package env

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// regKey is the per-user environment hive. Values written here are what
// setx writes and what a newly spawned process inherits.
const regKey = `Environment`

// Broadcast tells running applications that the environment changed.
//
// Without it, variables we write do not reach terminals opened afterwards
// until the user logs out and back in: Explorer caches the environment block
// and only refreshes on WM_SETTINGCHANGE. This is the same notification setx
// sends, and it is why our writes take effect immediately for new processes.
//
// SMTO_NORMAL is 0x0000, which is why it does not appear in the flag mask
// below: the only bit we set is SMTO_ABORTIFHUNG, so a hung window cannot
// block our exit.
const (
	wmSettingchange = 0x001A
	smtoAbortIfHung = 0x0002
	hwndBroadcast   = 0xffff
)

// Read returns the persisted value of name, or "" when unset. It reads the
// registry, which is the authority for what future processes see, rather than
// our own process environment.
func Read(name string) (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, regKey, registry.QUERY_VALUE)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", regKey, err)
	}
	defer k.Close()

	v, _, err := k.GetStringValue(name)
	if err != nil {
		if err == registry.ErrNotExist {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", name, err)
	}
	return expand(v), nil
}

// Write persists name=value and refreshes our own process environment.
//
// The registry stores REG_SZ and REG_EXPAND_SZ values; we normalise to
// REG_SZ unless the value contains %VAR% references, in which case
// REG_EXPAND_SZ is required for the loader to expand them.
func Write(name, value string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, regKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open %s for write: %w", regKey, err)
	}
	defer k.Close()

	if strings.Contains(value, "%") {
		if err := k.SetExpandStringValue(name, value); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	} else {
		if err := k.SetStringValue(name, value); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}

	if err := Set(name, value); err != nil {
		return err
	}
	Broadcast()
	return nil
}

// Delete removes a persisted variable and unsets it in our own process.
func Delete(name string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, regKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open %s for write: %w", regKey, err)
	}
	defer k.Close()

	if err := k.DeleteValue(name); err != nil && err != registry.ErrNotExist {
		return fmt.Errorf("delete %s: %w", name, err)
	}
	if err := Unset(name); err != nil {
		return err
	}
	Broadcast()
	return nil
}

// All returns every persisted user environment variable.
func All() (map[string]string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, regKey, registry.QUERY_VALUE)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", regKey, err)
	}
	defer k.Close()

	names, err := k.ReadValueNames(0)
	if err != nil {
		return nil, fmt.Errorf("enumerate %s: %w", regKey, err)
	}
	out := make(map[string]string, len(names))
	for _, n := range names {
		if v, _, err := k.GetStringValue(n); err == nil {
			out[n] = expand(v)
		}
	}
	return out, nil
}

// expand resolves %VAR% references using the current process environment.
// Registry values are frequently stored unexpanded, and comparing an
// unexpanded value against os.Getenv would always report a spurious change.
func expand(v string) string {
	if !strings.Contains(v, "%") {
		return v
	}
	out, err := registry.ExpandString(v)
	if err != nil {
		return v
	}
	return out
}

// user32 holds the one call we need to notify other applications.
var (
	user32           = windows.NewLazySystemDLL("user32")
	procSendMessageT = user32.NewProc("SendMessageTimeoutW")
)

// Broadcast sends WM_SETTINGCHANGE to every top-level window.
func Broadcast() {
	env, err := windows.UTF16PtrFromString("Environment")
	if err != nil {
		return
	}
	_, _, _ = procSendMessageT.Call(
		hwndBroadcast,
		wmSettingchange,
		0,
		uintptr(unsafe.Pointer(env)),
		smtoAbortIfHung,
		5000,
		0,
	)
}
