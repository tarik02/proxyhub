package entevents

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

var ErrShutdown = errors.New("shutdown")

const (
	EventTypeInit     = "init"
	EventTypeAdd      = "add"
	EventTypeDel      = "del"
	EventTypePing     = "ping"
	EventTypeShutdown = "shutdown"
)

type EntityEvent[T any] struct {
	ID      string
	Type    string
	Entity  T
	Payload any
}

type ClientChan[T any] chan EntityEvent[T]

type Manager[T any] struct {
	newClients    chan ClientChan[T]
	closedClients chan ClientChan[T]
	totalClients  map[ClientChan[T]]bool

	snapshot map[string]T

	events chan EntityEvent[T]

	shutdown      bool
	shutdownMu    sync.Mutex
	shutdownCh    chan struct{}
	shutdownErr   error
	shutdownErrMu sync.Mutex

	runDoneCh chan struct{}
	closedCh  chan struct{}
}

func New[T any](ctx context.Context) *Manager[T] {
	m := &Manager[T]{
		newClients:    make(chan ClientChan[T]),
		closedClients: make(chan ClientChan[T]),
		totalClients:  make(map[ClientChan[T]]bool),

		snapshot: make(map[string]T),

		events: make(chan EntityEvent[T]),

		shutdownCh: make(chan struct{}),
		runDoneCh:  make(chan struct{}),
		closedCh:   make(chan struct{}),
	}

	go m.run(ctx)

	return m
}

func (m *Manager[T]) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()

	case <-m.closedCh:
		return m.shutdownErr
	}
}

func (m *Manager[T]) Close() error {
	m.shutdownMu.Lock()
	defer m.shutdownMu.Unlock()

	if m.shutdown {
		return nil
	}
	m.shutdown = true

	m.shutdownErrMu.Lock()
	if m.shutdownErr == nil {
		m.shutdownErr = ErrShutdown
	}
	m.shutdownErrMu.Unlock()

	close(m.shutdownCh)

	<-m.runDoneCh

	close(m.closedCh)

	return nil
}

func (m *Manager[T]) CloseChan() <-chan struct{} {
	return m.shutdownCh
}

func (m *Manager[T]) Add(ctx context.Context, id string, entity T) error {
	return m.queueEvent(ctx, EntityEvent[T]{ID: id, Type: EventTypeAdd, Entity: entity, Payload: entity})
}

func (m *Manager[T]) Del(ctx context.Context, id string) error {
	p, _ := json.Marshal(id)
	return m.queueEvent(ctx, EntityEvent[T]{ID: id, Type: EventTypeDel, Payload: p})
}

func (e *Manager[T]) Update(ctx context.Context, event string, id string, entity T, payload any) error {
	return e.queueEvent(ctx, EntityEvent[T]{ID: id, Type: event, Entity: entity, Payload: payload})
}

func (e *Manager[T]) queueEvent(ctx context.Context, event EntityEvent[T]) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-e.shutdownCh:
		return e.shutdownErr
	case e.events <- event:
		return nil
	}
}

func (e *Manager[T]) run(ctx context.Context) {
	defer close(e.runDoneCh)

	t := time.Tick(30 * time.Second)

loop:
	for {
		select {
		case <-ctx.Done():
			return

		case <-e.shutdownCh:
			break loop

		case client := <-e.newClients:
			e.totalClients[client] = true
			res := make([]T, len(e.snapshot))
			i := 0
			for _, v := range e.snapshot {
				res[i] = v
				i++
			}
			client <- EntityEvent[T]{Type: EventTypeInit, Payload: res}

		case client := <-e.closedClients:
			delete(e.totalClients, client)
			close(client)

		case eventMsg := <-e.events:
			e.processEvent(eventMsg)

		case <-t:
			for client := range e.totalClients {
				client <- EntityEvent[T]{Type: EventTypePing, Payload: "null"}
			}
		}
	}

	for client := range e.totalClients {
		client <- EntityEvent[T]{Type: EventTypeShutdown, Payload: "null"}
		close(client)
	}
	e.totalClients = nil
}

func (e *Manager[T]) processEvent(event EntityEvent[T]) {
	switch event.Type {
	case EventTypeAdd:
		if _, ok := e.snapshot[event.ID]; ok {
			// ignore
			return
		}
		e.snapshot[event.ID] = event.Entity

	case EventTypeDel:
		if _, ok := e.snapshot[event.ID]; !ok {
			// ignore
			return
		}
		delete(e.snapshot, event.ID)

	default:
		if _, ok := e.snapshot[event.ID]; !ok {
			// ignore
			return
		}
		e.snapshot[event.ID] = event.Entity
	}

	for clientMessageChan := range e.totalClients {
		clientMessageChan <- event
	}
}

func (e *Manager[T]) ServeSnapshot(c *gin.Context) {
	clientChan := make(ClientChan[T], 32)

	select {
	case <-c.Request.Context().Done():
		c.Abort()
		return

	case <-e.shutdownCh:
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "service unavailable"})
		return

	case e.newClients <- clientChan:
	}

	var snapshot EntityEvent[T]
	select {
	case <-c.Request.Context().Done():
		c.Abort()
		return

	case <-e.shutdownCh:
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "service unavailable"})
		return

	case snapshot = <-clientChan:
	}

	go func() {
		select {
		case <-e.shutdownCh:
		case e.closedClients <- clientChan:
		}
	}()

	go func() {
		for range clientChan {
		}
	}()

	c.JSON(http.StatusOK, snapshot.Payload)
}

func (e *Manager[T]) ServeSSE(c *gin.Context) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("Transfer-Encoding", "chunked")

	clientChan := make(ClientChan[T], 32)

	select {
	case <-c.Request.Context().Done():
		c.Abort()
		return

	case <-e.shutdownCh:
		c.Status(http.StatusServiceUnavailable)
		return

	case e.newClients <- clientChan:
	}

	defer func() {
		// Drain client channel so that it does not block. Server may keep sending messages to this channel
		go func() {
			for range clientChan {
			}
		}()

		select {
		case <-e.shutdownCh:
		case e.closedClients <- clientChan:
		}
	}()

	c.Stream(func(w io.Writer) bool {
		if msg, ok := <-clientChan; ok {
			c.SSEvent(msg.Type, msg.Payload)
			return true
		}
		return false
	})
}
