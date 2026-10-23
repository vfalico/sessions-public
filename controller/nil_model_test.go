package controller

import (
	"context"
	"github.com/agent-primitives/sessions/api"
	"strings"
	"testing"
)

func TestUnconfiguredModelRefusesWithoutPanic(t *testing.T) {
	c := &Controller{}
	if _, err := c.invokeModel(context.Background(), "execution", api.ModelRequest{}); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatal(err)
	}
}
