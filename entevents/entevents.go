package entevents

import (
	"context"
	"io"
	"sync"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	EventTypeAdd = "add"
	EventTypeDel = "del"
)

type EntityEvent[T any] struct {
	ID      string
	Type    string
	Entity  T
	Payload any
}

type ClientChan[T any] chan EntityEvent[T]

type Manager[T any] struct {
	NewClients    chan ClientChan[T]
	ClosedClients chan ClientChan[T]
	TotalClients  map[ClientChan[T]]bool

	Snapshot map[string]T

	Events chan EntityEvent[T]

	closed atomic.Bool
	mu     sync.Mutex
}

func New[T any]() *Manager[T] {
	return &Manager[T]{
		NewClients:    make(chan ClientChan[T]),
		ClosedClients: make(chan ClientChan[T]),
		TotalClients:  make(map[ClientChan[T]]bool),

		Snapshot: make(map[string]T),

		Events: make(chan EntityEvent[T]),
	}
}

func (e *Manager[T]) Add(id string, entity T) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed.Load() {
		return
	}

	e.Events <- EntityEvent[T]{ID: id, Type: EventTypeAdd, Entity: entity, Payload: entity}
}

func (e *Manager[T]) Del(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed.Load() {
		return
	}

	e.Events <- EntityEvent[T]{ID: id, Type: EventTypeDel, Payload: id}
}

func (e *Manager[T]) Update(id string, entity T, payload any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed.Load() {
		return
	}

	e.Events <- EntityEvent[T]{ID: id, Entity: entity, Payload: payload}
}

func (e *Manager[T]) Run(ctx context.Context) error {
	defer func() {
		e.mu.Lock()
		e.closed.Store(true)
		e.mu.Unlock()

		defer close(e.NewClients)
		defer close(e.ClosedClients)
		defer close(e.Events)

	loop:
		for {
			select {
			case client := <-e.NewClients:
				close(client)

			case client := <-e.ClosedClients:
				close(client)

			case <-e.Events:
				continue

			default:
				break loop
			}
		}

		for client := range e.TotalClients {
			delete(e.TotalClients, client)
			close(client)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case client := <-e.NewClients:
			e.TotalClients[client] = true
			res := make([]T, len(e.Snapshot))
			i := 0
			for _, v := range e.Snapshot {
				res[i] = v
				i++
			}
			client <- EntityEvent[T]{Type: "init", Payload: res}

		case client := <-e.ClosedClients:
			delete(e.TotalClients, client)
			close(client)

		case eventMsg := <-e.Events:
			e.processEvent(eventMsg)
		}
	}
}

func (e *Manager[T]) processEvent(event EntityEvent[T]) {
	switch event.Type {
	case EventTypeAdd:
		if _, ok := e.Snapshot[event.ID]; ok {
			// ignore
			return
		}
		e.Snapshot[event.ID] = event.Entity

	case EventTypeDel:
		if _, ok := e.Snapshot[event.ID]; !ok {
			// ignore
			return
		}
		delete(e.Snapshot, event.ID)

	default:
		if _, ok := e.Snapshot[event.ID]; !ok {
			// ignore
			return
		}
		e.Snapshot[event.ID] = event.Entity
	}

	for clientMessageChan := range e.TotalClients {
		clientMessageChan <- event
	}
}

func (e *Manager[T]) ServeSnapshot(c *gin.Context) {
	clientChan := make(ClientChan[T])
	e.NewClients <- clientChan
	snapshot := <-clientChan

	go func() {
		for range clientChan {
		}
	}()
	e.mu.Lock()
	if !e.closed.Load() {
		e.ClosedClients <- clientChan
	}
	e.mu.Unlock()

	zap.S().Debugf("serving snapshot: %v", snapshot.Payload)
	c.JSON(200, snapshot.Payload)
}

func (e *Manager[T]) ServeSSE(c *gin.Context) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("Transfer-Encoding", "chunked")

	clientChan := make(ClientChan[T])
	e.NewClients <- clientChan

	defer func() {
		// Drain client channel so that it does not block. Server may keep sending messages to this channel
		go func() {
			for range clientChan {
			}
		}()
		// Send closed connection to event server
		e.mu.Lock()
		if !e.closed.Load() {
			e.ClosedClients <- clientChan
		}
		e.mu.Unlock()
	}()

	c.Stream(func(w io.Writer) bool {
		if msg, ok := <-clientChan; ok {
			c.SSEvent(msg.Type, msg.Payload)
			return true
		}
		return false
	})
}
