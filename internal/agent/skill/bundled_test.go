package skill

import (
	"context"
	"strings"
	"testing"
)

func TestBundledSkills(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	catalog := NewCatalog(Bundled(cwd))
	for _, name := range []string{"debug", "init", "refactor", "review"} {
		if spec, ok := catalog.Get(name); !ok || !spec.Privileged || spec.BaseDir != cwd {
			t.Errorf("bundled %s = %+v, %v", name, spec, ok)
		}
	}
	// /init is the user's to run: the model would rewrite AGENTS.md unasked.
	if spec, _ := catalog.Get("init"); !spec.DisableModelInvocation {
		t.Error("the model can invoke init")
	}

	inv, err := catalog.Invoke(context.Background(), InvokeInput{Name: "debug", Args: "failing test", By: ByUser})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(inv.Prompt, "failing test") || !strings.Contains(inv.Prompt, `<skill name="debug">`) {
		t.Fatalf("expected the wrapped prompt with its args, got %q", inv.Prompt)
	}
}
