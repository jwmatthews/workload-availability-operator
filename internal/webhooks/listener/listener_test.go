package listener

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
)

func TestNewAdmissionClientRejectsMissingInputs(t *testing.T) {
	if _, err := NewAdmissionClient(nil, runtime.NewScheme()); err == nil {
		t.Error("NewAdmissionClient accepted a nil config")
	}
	if _, err := NewAdmissionClient(&rest.Config{Host: "127.0.0.1:1"}, nil); err == nil {
		t.Error("NewAdmissionClient accepted a nil scheme")
	}
	if _, err := NewAdmissionClient(&rest.Config{Host: "127.0.0.1:1"}, runtime.NewScheme()); err != nil {
		t.Errorf("NewAdmissionClient failed for a valid config: %v", err)
	}
}

func TestAddReadyCheckRejectsNilManager(t *testing.T) {
	if err := AddReadyCheck(nil); err == nil {
		t.Error("AddReadyCheck accepted a nil manager")
	}
}
