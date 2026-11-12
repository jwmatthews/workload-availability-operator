package inventory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	farapi "github.com/medik8s/fence-agents-remediation/v5/api/v1alpha1"
	"github.com/medik8s/fence-agents-remediation/v5/pkg/validation"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const ipmilan = "fence_ipmilan"

func writeInventory(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agents.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func names(count int) string {
	var b strings.Builder
	for i := range count {
		fmt.Fprintf(&b, "fence_agent_%04d\n", i)
	}
	return b.String()
}

func TestLoadRejectsIncompleteOrInvalidFiles(t *testing.T) {
	for name, content := range map[string]string{
		"empty file":             "",
		"only a newline":         "\n",
		"no final newline":       "fence_ipmilan\nfence_aws",
		"empty line":             "fence_ipmilan\n\nfence_aws\n",
		"duplicate name":         "fence_ipmilan\nfence_aws\nfence_ipmilan\n",
		"name without prefix":    "ipmilan\n",
		"prefix only":            "fence_\n",
		"name with a slash":      "fence_wao/child\n",
		"path name":              "/usr/sbin/fence_ipmilan\n",
		"name with a space":      "fence_ipmilan extra\n",
		"name with a tab":        "fence_ipmilan\textra\n",
		"carriage return":        "fence_ipmilan\r\n",
		"leading space":          " fence_ipmilan\n",
		"name of 256 bytes":      "fence_" + strings.Repeat("a", 250) + "\n",
		"invalid UTF-8":          "fence_\xff\xfe\n",
		"1025 names":             names(1025),
		"file larger than limit": "fence_ipmilan\n" + strings.Repeat("\n", maxFileBytes),
		"shell error text":       "ls: cannot access 'fence_*': No such file or directory\n",
	} {
		t.Run(name, func(t *testing.T) {
			check, err := Load(writeInventory(t, content))
			if err == nil {
				t.Fatal("Load accepted the file")
			}
			if check != nil {
				t.Fatal("Load returned a checker together with an error")
			}
		})
	}
}

func TestLoadRejectsMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "agents.txt")); err == nil {
		t.Fatal("Load accepted a missing file")
	}
}

func TestLoadRejectsDirectory(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("Load accepted a directory")
	}
}

func TestLoadRejectsUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a file without read permission")
	}
	path := writeInventory(t, ipmilan+"\n")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted an unreadable file")
	}
}

func TestLoadAcceptsBoundaryFiles(t *testing.T) {
	longest := "fence_" + strings.Repeat("a", 249)
	for name, content := range map[string]string{
		"one name":           ipmilan + "\n",
		"name of 255 bytes":  longest + "\n",
		"1024 names":         names(1024),
		"non-ASCII name":     "fence_ünïcode\n",
		"names with symbols": "fence_a-b.c_d\nfence_1\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeInventory(t, content)); err != nil {
				t.Fatalf("Load rejected the file: %v", err)
			}
		})
	}
}

// The rows follow the approved difference table in docs/far-webhooks.md.
func TestCheckerAcceptsExactListedNamesOnly(t *testing.T) {
	// fence_broken_link represents a broken symlink in the image: the collector lists its name.
	check, err := Load(writeInventory(t, "fence_aws\nfence_broken_link\n"+ipmilan+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	for input, want := range map[string]bool{
		ipmilan:                              true,
		"fence_aws":                          true,
		"fence_broken_link":                  true,
		"fence_wao_missing":                  false,
		"fence_ipmi":                         false,
		"fence_ipmilan2":                     false,
		"FENCE_IPMILAN":                      false,
		"":                                   false,
		" " + ipmilan:                        false,
		ipmilan + "\n":                       false,
		"./fence_ipmilan":                    false,
		"fence_ipmilan/":                     false,
		"/fence_ipmilan":                     false,
		"fence_wao_missing/../fence_ipmilan": false,
		"fence_wao/..":                       false,
		"fence_wao/../../bin/sh":             false,
		"/usr/sbin/fence_ipmilan":            false,
		"fence_ipmilan/wao-child":            false,
		"fence_ipmilan\x00":                  false,
		"fence_" + strings.Repeat("a", 5000): false,
	} {
		got, err := check(input)
		if err != nil {
			t.Errorf("check(%q) returned error %v; the checker never returns an error", input, err)
		}
		if got != want {
			t.Errorf("check(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestCheckerIsASnapshot(t *testing.T) {
	path := writeInventory(t, ipmilan+"\n")
	check, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fence_aws\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, _ := check(ipmilan); !ok {
		t.Error("a name from Pod start is no longer accepted after the file changed")
	}
	if ok, _ := check("fence_aws"); ok {
		t.Error("a name added after Load is accepted")
	}
}

func TestCheckerConcurrentLookups(t *testing.T) {
	check, err := Load(writeInventory(t, names(1024)))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for worker := range 16 {
		wg.Go(func() {
			for i := range 2000 {
				listed := fmt.Sprintf("fence_agent_%04d", (i+worker)%1024)
				if ok, err := check(listed); !ok || err != nil {
					t.Errorf("check(%q) = %v, %v", listed, ok, err)
					return
				}
				if ok, _ := check(listed + "/.."); ok {
					t.Errorf("check accepted %q", listed+"/..")
					return
				}
			}
		})
	}
	wg.Wait()
}

// The imported FAR validator must report an unlisted agent with its public message.
func TestUnlistedAgentGivesFARValidationMessage(t *testing.T) {
	check, err := Load(writeInventory(t, ipmilan+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	farapi.SetAgentValidator(validation.NewCustomAgentValidator(check))
	t.Cleanup(func() { farapi.SetAgentValidator(validation.NewAgentValidator()) })

	far := &farapi.FenceAgentsRemediation{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-test", Namespace: "far-test"},
		Spec: farapi.FenceAgentsRemediationSpec{
			Agent:               "./fence_ipmilan",
			RemediationStrategy: farapi.ResourceDeletionRemediationStrategy,
		},
	}
	// The agent check runs before any client use, so a nil client is sufficient here.
	_, err = (&farapi.FARValidator{}).ValidateCreate(context.Background(), far)
	if err == nil || !strings.Contains(err.Error(), "unsupported fence agent: ./fence_ipmilan") {
		t.Fatalf("expected the unsupported-agent message, got %v", err)
	}
}
