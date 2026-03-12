package provider

import (
	"testing"
)

func TestRateLimiter_Allow(t *testing.T) {
	rl := NewRateLimiter(10, 5)
	
	for i := 0; i < 10; i++ {
		result := rl.Allow("test-agent")
		if result != true {
			t.Errorf("Expected Allow to return true on request %d, got %v", i+1, result)
		}
	}
	
	result := rl.Allow("test-agent")
	if result != false {
		t.Errorf("Expected Allow to return false after limit exceeded, got %v", result)
	}
}

func TestRateLimiter_MultipleAgents(t *testing.T) {
	rl := NewRateLimiter(5, 5)
	
	for i := 0; i < 5; i++ {
		if !rl.Allow("agent1") {
			t.Errorf("Expected agent1 request %d to be allowed", i+1)
		}
		if !rl.Allow("agent2") {
			t.Errorf("Expected agent2 request %d to be allowed", i+1)
		}
	}
	
	if rl.Allow("agent1") {
		t.Error("Expected agent1 to be rate limited")
	}
	if rl.Allow("agent2") {
		t.Error("Expected agent2 to be rate limited")
	}
}

func TestRateLimiter_ConcurrentAccess(t *testing.T) {
	rl := NewRateLimiter(100, 10)
	
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 10; j++ {
				rl.Allow("concurrent-agent")
			}
			done <- true
		}()
	}
	
	for i := 0; i < 10; i++ {
		<-done
	}
}
