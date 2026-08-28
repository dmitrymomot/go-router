package main

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestRoomDeliveryIsBestEffortForSlowListeners(t *testing.T) {
	rm := newRoom()
	slow, leaveSlow := rm.join()
	defer leaveSlow()

	for i := range listenerBuffer {
		rm.broadcast(message{Text: strconv.Itoa(i)})
	}
	if got := len(slow); got != listenerBuffer {
		t.Fatalf("slow listener buffered %d messages, want %d", got, listenerBuffer)
	}

	fast, leaveFast := rm.join()
	defer leaveFast()
	want := message{Text: "latest"}
	done := make(chan struct{})
	go func() {
		rm.broadcast(want)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a slow listener blocked the broadcast")
	}
	select {
	case got := <-fast:
		if got != want {
			t.Errorf("fast listener got %#v, want %#v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("fast listener missed the broadcast")
	}
	for range listenerBuffer {
		if got := <-slow; got == want {
			t.Error("full slow listener received the latest message")
		}
	}
	select {
	case got := <-slow:
		t.Errorf("slow listener unexpectedly buffered %#v", got)
	default:
	}
}

func TestRoomShutdownClosesCurrentAndFutureListeners(t *testing.T) {
	rm := newRoom()
	first, leaveFirst := rm.join()
	second, leaveSecond := rm.join()

	rm.close()
	rm.close()
	leaveFirst()
	leaveSecond()
	rm.broadcast(message{Text: "after close"})

	wantClosed(t, first)
	wantClosed(t, second)
	future, leaveFuture := rm.join()
	defer leaveFuture()
	wantClosed(t, future)
}

func TestRoomConcurrentJoinBroadcastLeaveAndShutdown(t *testing.T) {
	rm := newRoom()
	var wg sync.WaitGroup
	for i := range 128 {
		wg.Go(func() {
			messages, leave := rm.join()
			rm.broadcast(message{Text: strconv.Itoa(i)})
			select {
			case <-messages:
			default:
			}
			leave()
		})
	}
	wg.Go(rm.close)
	wg.Wait()
	if got := roomReaders(rm); got != 0 {
		t.Errorf("readers after concurrent shutdown = %d", got)
	}
}

func wantClosed(t *testing.T, ch <-chan message) {
	t.Helper()
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("listener remained open")
		}
	case <-time.After(time.Second):
		t.Error("listener did not close")
	}
}
