package entevents

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestServeSnapshotNotBlockedBySlowSubscriber(t *testing.T) {
	gin.SetMode(gin.TestMode)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := New[string](ctx)
	defer events.Close()

	slowCh, unsubscribe, err := events.Subscribe(ctx, 1)
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	defer unsubscribe()

	if err := events.Add(ctx, "proxy-1", "proxy-1"); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/proxies", nil)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = req

	doneCh := make(chan struct{})
	go func() {
		events.ServeSnapshot(c)
		close(doneCh)
	}()

	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeSnapshot() blocked behind a slow subscriber")
	}

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var snapshot []string
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if !reflect.DeepEqual(snapshot, []string{"proxy-1"}) {
		t.Fatalf("snapshot = %#v, want %#v", snapshot, []string{"proxy-1"})
	}

	initEvent := mustRecvEntityEvent(t, slowCh)
	if initEvent.Type != EventTypeInit {
		t.Fatalf("slow subscriber event type = %q, want %q", initEvent.Type, EventTypeInit)
	}
	mustRecvClosedEntityEvent(t, slowCh)
}

func TestSubscribeEvictsSlowSubscriber(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := New[string](ctx)
	defer events.Close()

	slowCh, slowUnsubscribe, err := events.Subscribe(ctx, 1)
	if err != nil {
		t.Fatalf("Subscribe(slow) error = %v", err)
	}
	defer slowUnsubscribe()

	fastCh, fastUnsubscribe, err := events.Subscribe(ctx, 2)
	if err != nil {
		t.Fatalf("Subscribe(fast) error = %v", err)
	}
	defer fastUnsubscribe()

	initEvent := mustRecvEntityEvent(t, fastCh)
	if initEvent.Type != EventTypeInit {
		t.Fatalf("fast init event type = %q, want %q", initEvent.Type, EventTypeInit)
	}

	if err := events.Add(ctx, "proxy-1", "proxy-1"); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	addEvent := mustRecvEntityEvent(t, fastCh)
	if addEvent.Type != EventTypeAdd {
		t.Fatalf("fast add event type = %q, want %q", addEvent.Type, EventTypeAdd)
	}
	if addEvent.Entity != "proxy-1" {
		t.Fatalf("fast add event entity = %q, want %q", addEvent.Entity, "proxy-1")
	}

	slowInit := mustRecvEntityEvent(t, slowCh)
	if slowInit.Type != EventTypeInit {
		t.Fatalf("slow init event type = %q, want %q", slowInit.Type, EventTypeInit)
	}
	mustRecvClosedEntityEvent(t, slowCh)

	if err := events.Update(ctx, "update", "proxy-1", "proxy-1-updated", "proxy-1-updated"); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	updateEvent := mustRecvEntityEvent(t, fastCh)
	if updateEvent.Type != "update" {
		t.Fatalf("fast update event type = %q, want %q", updateEvent.Type, "update")
	}
	if updateEvent.Entity != "proxy-1-updated" {
		t.Fatalf("fast update event entity = %q, want %q", updateEvent.Entity, "proxy-1-updated")
	}
}

func mustRecvEntityEvent[T any](t *testing.T, ch <-chan EntityEvent[T]) EntityEvent[T] {
	t.Helper()

	type result[T any] struct {
		event EntityEvent[T]
		ok    bool
	}

	resCh := make(chan result[T], 1)
	go func() {
		event, ok := <-ch
		resCh <- result[T]{event: event, ok: ok}
	}()

	select {
	case res := <-resCh:
		if !res.ok {
			t.Fatal("channel closed before event arrived")
		}
		return res.event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
		return EntityEvent[T]{}
	}
}

func mustRecvClosedEntityEvent[T any](t *testing.T, ch <-chan EntityEvent[T]) {
	t.Helper()

	closedCh := make(chan bool, 1)
	go func() {
		_, ok := <-ch
		closedCh <- ok
	}()

	select {
	case ok := <-closedCh:
		if ok {
			t.Fatal("channel stayed open")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for channel to close")
	}
}
