package infrastructure_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/uservseatb/apinfra/internal/infrastructure"
)

const (
	composeV1 = "services:\n  web:\n    image: nginx:1\n"
	composeV2 = "services:\n  web:\n    image: nginx:2\n"
	envV1     = "TOKEN=one\n"
	envV2     = "TOKEN=two\n"
)

func newStackFS(t *testing.T) (*infrastructure.StackFS, string) {
	t.Helper()

	infra := filepath.Join(t.TempDir(), "infra")

	err := os.MkdirAll(infra, 0o750)
	if err != nil {
		t.Fatalf("mkdir infra: %v", err)
	}

	return infrastructure.NewStackFS(infra), infra
}

func TestStackFS_WriteAppStack_IsIdempotentAndKeepsEnvPrivate(t *testing.T) {
	t.Parallel()

	stack, infra := newStackFS(t)
	appDir := filepath.Join(infra, "apps", "shop")

	changed, err := stack.WriteAppStack("shop", composeV1, envV1)
	if err != nil || !changed {
		t.Fatalf("first write: changed=%v err=%v", changed, err)
	}

	envPath := filepath.Join(appDir, ".env")
	composePath := filepath.Join(appDir, "docker-compose.yml")

	old := time.Now().Add(-time.Hour).Truncate(time.Second)

	for _, path := range []string{envPath, composePath} {
		err = os.Chtimes(path, old, old)
		if err != nil {
			t.Fatalf("Chtimes: %v", err)
		}
	}

	changed, err = stack.WriteAppStack("shop", composeV1, envV1)
	if err != nil || changed {
		t.Fatalf("repeat write: changed=%v err=%v, want false/nil", changed, err)
	}

	for _, path := range []string{envPath, composePath} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("Stat: %v", statErr)
		}

		if !info.ModTime().Equal(old) {
			t.Fatalf("%s rewritten on identical input", path)
		}
	}

	// .env is read-only (0400); updating it must still work and keep the mode.
	changed, err = stack.WriteAppStack("shop", composeV1, envV2)
	if err != nil || !changed {
		t.Fatalf("env update: changed=%v err=%v", changed, err)
	}

	if got := readTestFile(t, envPath); got != envV2 {
		t.Fatalf(".env = %q, want %q", got, envV2)
	}

	envInfo, err := os.Stat(envPath)
	if err != nil || envInfo.Mode().Perm() != 0o400 {
		t.Fatalf(".env mode = %v err=%v, want 0400", envInfo.Mode().Perm(), err)
	}

	composeInfo, err := os.Stat(composePath)
	if err != nil || composeInfo.Mode().Perm() != 0o400 {
		t.Fatalf("compose mode = %v err=%v, want 0400", composeInfo.Mode().Perm(), err)
	}

	assertOwnerOnlyDir(t, filepath.Join(infra, "apps"))
	assertOwnerOnlyDir(t, appDir)

	changed, err = stack.WriteAppStack("shop", composeV2, envV2)
	if err != nil || !changed {
		t.Fatalf("compose update: changed=%v err=%v", changed, err)
	}
}

func TestStackFS_WriteAppStack_HealsLegacyPermissiveModes(t *testing.T) {
	t.Parallel()

	stack, infra := newStackFS(t)
	appsRoot := filepath.Join(infra, "apps")
	appDir := filepath.Join(appsRoot, "shop")
	composePath := filepath.Join(appDir, "docker-compose.yml")
	envPath := filepath.Join(appDir, ".env")

	err := os.MkdirAll(appDir, 0o755)
	if err != nil {
		t.Fatalf("seed app dir: %v", err)
	}

	seedWorldReadable(t, composePath, composeV1)
	seedWorldReadable(t, envPath, envV1)

	changed, err := stack.WriteAppStack("shop", composeV1, envV1)
	if err != nil || changed {
		t.Fatalf("identical heal write: changed=%v err=%v, want false/nil", changed, err)
	}

	assertOwnerOnlyDir(t, appsRoot)
	assertOwnerOnlyDir(t, appDir)

	composeInfo, err := os.Stat(composePath)
	if err != nil || composeInfo.Mode().Perm() != 0o400 {
		t.Fatalf("compose mode = %v err=%v, want 0400", composeInfo.Mode().Perm(), err)
	}

	envInfo, err := os.Stat(envPath)
	if err != nil || envInfo.Mode().Perm() != 0o400 {
		t.Fatalf(".env mode = %v err=%v, want 0400", envInfo.Mode().Perm(), err)
	}
}

func seedWorldReadable(t *testing.T, path, content string) {
	t.Helper()

	err := os.WriteFile(path, []byte(content), 0o600)
	if err != nil {
		t.Fatalf("seed %s: %v", path, err)
	}

	err = os.Chmod(path, 0o644)
	if err != nil {
		t.Fatalf("chmod 0644 %s: %v", path, err)
	}
}

func assertOwnerOnlyDir(t *testing.T, dir string) {
	t.Helper()

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat(%s): %v", dir, err)
	}

	if info.Mode().Perm() != 0o700 {
		t.Fatalf("%s mode = %v, want 0700", dir, info.Mode().Perm())
	}
}

func TestStackFS_WriteProxyCompose_IsIdempotent(t *testing.T) {
	t.Parallel()

	stack, infra := newStackFS(t)

	for idx, want := range []bool{true, false} {
		changed, err := stack.WriteProxyCompose(composeV1)
		if err != nil || changed != want {
			t.Fatalf("write #%d: changed=%v err=%v, want %v", idx+1, changed, err, want)
		}
	}

	if got := readTestFile(t, filepath.Join(infra, "docker-compose.yml")); got != composeV1 {
		t.Fatalf("proxy compose = %q", got)
	}

	changed, err := stack.WriteProxyCompose(composeV2)
	if err != nil || !changed {
		t.Fatalf("content change: changed=%v err=%v", changed, err)
	}
}

func TestStackFS_AppLifecycle(t *testing.T) {
	t.Parallel()

	stack, infra := newStackFS(t)

	dirs, err := stack.ListAppDirs()
	if err != nil || len(dirs) != 0 {
		t.Fatalf("no apps dir yet: dirs=%v err=%v", dirs, err)
	}

	for _, name := range []string{"shop", "blog"} {
		_, err = stack.WriteAppStack(name, composeV1, envV1)
		if err != nil {
			t.Fatalf("WriteAppStack(%s): %v", name, err)
		}
	}

	writeTestFile(t, filepath.Join(infra, "apps", "README"), "not an app")

	dirs, err = stack.ListAppDirs()
	if err != nil {
		t.Fatalf("ListAppDirs: %v", err)
	}

	slices.Sort(dirs)

	if !slices.Equal(dirs, []string{"blog", "shop"}) {
		t.Fatalf("dirs = %v, want [blog shop] (files ignored)", dirs)
	}

	has, err := stack.AppComposeExists("shop")
	if err != nil || !has {
		t.Fatalf("AppComposeExists(shop) = %v, %v", has, err)
	}

	has, err = stack.AppComposeExists("ghost")
	if err != nil || has {
		t.Fatalf("AppComposeExists(ghost) = %v, %v, want false/nil", has, err)
	}

	err = stack.RemoveAppDir("shop")
	if err != nil {
		t.Fatalf("RemoveAppDir: %v", err)
	}

	has, err = stack.AppComposeExists("shop")
	if err != nil || has {
		t.Fatalf("after remove AppComposeExists(shop) = %v, %v", has, err)
	}

	if !exists(filepath.Join(infra, "apps", "blog", "docker-compose.yml")) {
		t.Fatal("removing shop must not touch blog")
	}

	err = stack.RemoveAppDir("shop")
	if err != nil {
		t.Fatalf("removing absent app must be a no-op, got %v", err)
	}
}

func TestStackFS_RejectsUnsafeProjectNames(t *testing.T) {
	t.Parallel()

	names := []string{"", "..", "../escape", "a/b", "/abs", ".hidden", "-lead", "with space", "x/../../y"}

	for _, name := range names {
		t.Run("name_"+name, func(t *testing.T) {
			t.Parallel()

			stack, infra := newStackFS(t)
			parent := filepath.Dir(infra)

			_, err := stack.WriteAppStack(name, composeV1, envV1)
			if err == nil {
				t.Fatal("WriteAppStack must reject")
			}

			_, err = stack.AppComposeExists(name)
			if err == nil {
				t.Fatal("AppComposeExists must reject")
			}

			err = stack.MarkApplyPending(name)
			if err == nil {
				t.Fatal("MarkApplyPending must reject")
			}

			_, err = stack.HasApplyPending(name)
			if err == nil {
				t.Fatal("HasApplyPending must reject")
			}

			err = stack.ClearApplyPending(name)
			if err == nil {
				t.Fatal("ClearApplyPending must reject")
			}

			err = stack.RemoveAppDir(name)
			if err == nil {
				t.Fatal("RemoveAppDir must reject")
			}

			entries, err := os.ReadDir(parent)
			if err != nil {
				t.Fatalf("ReadDir: %v", err)
			}

			if len(entries) != 1 {
				t.Fatalf("files created outside infra: %v", entries)
			}
		})
	}
}

func TestStackFS_ApplyPending_MarkBeforeWriteAndClear(t *testing.T) {
	t.Parallel()

	stack, infra := newStackFS(t)
	pendingPath := filepath.Join(infra, "apps", "shop", ".apply_pending")

	has, err := stack.HasApplyPending("shop")
	if err != nil || has {
		t.Fatalf("HasApplyPending before write = %v, %v", has, err)
	}

	changed, err := stack.WriteAppStack("shop", composeV1, envV1)
	if err != nil || !changed {
		t.Fatalf("first write: changed=%v err=%v", changed, err)
	}

	has, err = stack.HasApplyPending("shop")
	if err != nil || !has {
		t.Fatalf("content change must create pending: has=%v err=%v", has, err)
	}

	info, err := os.Stat(pendingPath)
	if err != nil {
		t.Fatalf("stat pending: %v", err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Fatalf("pending mode = %v, want 0600", info.Mode().Perm())
	}

	changed, err = stack.WriteAppStack("shop", composeV1, envV1)
	if err != nil || changed {
		t.Fatalf("identical write: changed=%v err=%v", changed, err)
	}

	has, err = stack.HasApplyPending("shop")
	if err != nil || !has {
		t.Fatalf("identical write must keep pending: has=%v err=%v", has, err)
	}

	err = stack.ClearApplyPending("shop")
	if err != nil {
		t.Fatalf("ClearApplyPending: %v", err)
	}

	has, err = stack.HasApplyPending("shop")
	if err != nil || has {
		t.Fatalf("after clear HasApplyPending = %v, %v", has, err)
	}

	err = stack.ClearApplyPending("shop")
	if err != nil {
		t.Fatalf("clear absent pending must be a no-op: %v", err)
	}

	changed, err = stack.WriteAppStack("shop", composeV2, envV1)
	if err != nil || !changed {
		t.Fatalf("compose update: changed=%v err=%v", changed, err)
	}

	has, err = stack.HasApplyPending("shop")
	if err != nil || !has {
		t.Fatalf("compose update must set pending again: has=%v err=%v", has, err)
	}

	err = stack.RemoveAppDir("shop")
	if err != nil {
		t.Fatalf("RemoveAppDir: %v", err)
	}

	if exists(pendingPath) {
		t.Fatal("RemoveAppDir must remove pending with the app dir")
	}
}
