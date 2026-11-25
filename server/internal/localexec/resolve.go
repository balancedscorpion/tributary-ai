package localexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type commandDirsFunc func(goos string) []string

var extraCommandDirs commandDirsFunc = defaultExtraCommandDirs

// CommandSearchDirs returns the fallback search directories for the current OS.
// Callers that launch resolved commands can prepend these to PATH so child
// processes have the same GUI-app-friendly lookup behavior.
func CommandSearchDirs() []string {
	return CommandSearchDirsForOS(runtime.GOOS)
}

func CommandSearchDirsForOS(goos string) []string {
	dirs := append([]string(nil), extraCommandDirs(goos)...)
	dirs = append(dirs, bundledRuntimeCommandDirs(goos)...)
	return dedupeCommandDirs(dirs)
}

// ResolveCommand finds a local executable. It respects PATH first, then checks
// common install locations that macOS GUI apps often miss.
func ResolveCommand(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	if hasPathSeparator(name) {
		if executableFile(name) {
			return name, true
		}
		return "", false
	}
	if path, err := exec.LookPath(name); err == nil && path != "" {
		return path, true
	}

	seen := map[string]bool{}
	for _, dir := range CommandSearchDirsForOS(runtime.GOOS) {
		dir = strings.TrimSpace(dir)
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		path := filepath.Join(dir, name)
		if executableFile(path) {
			return path, true
		}
		if runtime.GOOS == "windows" && filepath.Ext(path) == "" {
			for _, ext := range []string{".exe", ".cmd", ".bat"} {
				if executableFile(path + ext) {
					return path + ext, true
				}
			}
		}
	}
	return "", false
}

func dedupeCommandDirs(dirs []string) []string {
	if len(dirs) == 0 {
		return nil
	}
	out := make([]string, 0, len(dirs))
	seen := map[string]bool{}
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		clean := filepath.Clean(dir)
		key := clean
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, clean)
	}
	return out
}

func bundledRuntimeCommandDirs(goos string) []string {
	return runtimeCommandDirsFromBases(goos, bundledRuntimeBases())
}

func bundledRuntimeBases() []string {
	bases := []string(nil)
	add := func(base string) {
		base = strings.TrimSpace(base)
		if base != "" {
			bases = append(bases, base)
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		add(cwd)
	}
	if executable, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(executable)
		add(exeDir)
		add(filepath.Join(exeDir, "runtime"))
		add(filepath.Join(exeDir, "resources", "runtime"))
		add(filepath.Join(exeDir, "..", "Resources", "runtime"))
		add(filepath.Join(exeDir, "..", "Resources"))
	}
	return bases
}

func runtimeCommandDirsFromBases(goos string, bases []string) []string {
	dirs := make([]string, 0, len(bases)*10)
	for _, base := range bases {
		base = strings.TrimSpace(base)
		if base == "" {
			continue
		}
		dirs = append(dirs,
			filepath.Join(base, "bin"),
			filepath.Join(base, "native", "tools", "bin"),
			filepath.Join(base, "native", "poppler", "bin"),
			filepath.Join(base, "native", "poppler", "poppler", "bin"),
			filepath.Join(base, "native", "tesseract", "bin"),
			filepath.Join(base, "native", "tesseract", "tesseract", "bin"),
			filepath.Join(base, "native", "search", "bin"),
			filepath.Join(base, "poppler", "bin"),
			filepath.Join(base, "tesseract", "bin"),
		)
		if goos == "windows" {
			dirs = append(dirs,
				filepath.Join(base, "native", "tools", "Library", "bin"),
				filepath.Join(base, "native", "poppler", "Library", "bin"),
				filepath.Join(base, "native", "tesseract", "Library", "bin"),
				filepath.Join(base, "native", "search", "Library", "bin"),
				filepath.Join(base, "Library", "bin"),
			)
		}
	}
	return dedupeCommandDirs(dirs)
}

// CommandContext is exec.CommandContext with the same command lookup behavior
// as ResolveCommand.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	var cmd *exec.Cmd
	if path, ok := ResolveCommand(name); ok {
		cmd = exec.CommandContext(ctx, path, args...)
	} else {
		cmd = exec.CommandContext(ctx, name, args...)
	}
	configureCommand(cmd)
	return cmd
}

func hasPathSeparator(name string) bool {
	return strings.ContainsRune(name, '/') || strings.ContainsRune(name, '\\')
}

func executableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode().Perm()&0o111 != 0
}

func defaultExtraCommandDirs(goos string) []string {
	switch goos {
	case "darwin":
		return []string{
			"/opt/homebrew/bin",
			"/usr/local/bin",
			"/opt/local/bin",
			"/usr/bin",
			"/bin",
			"/usr/sbin",
			"/sbin",
		}
	case "linux":
		return []string{
			"/usr/local/bin",
			"/usr/bin",
			"/bin",
			"/snap/bin",
			"/nix/var/nix/profiles/default/bin",
			"/run/current-system/sw/bin",
		}
	case "windows":
		dirs := make([]string, 0, 16)
		if base := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); base != "" {
			dirs = append(dirs,
				filepath.Join(base, "Programs", "Git", "cmd"),
				filepath.Join(base, "Programs", "Git", "bin"),
				filepath.Join(base, "GitHub CLI"),
			)
			dirs = append(dirs, filepath.Join(base, "Google", "Cloud SDK", "google-cloud-sdk", "bin"))
		}
		if base := strings.TrimSpace(os.Getenv("ProgramFiles")); base != "" {
			dirs = append(dirs,
				filepath.Join(base, "Git", "cmd"),
				filepath.Join(base, "Git", "bin"),
				filepath.Join(base, "GitHub CLI"),
			)
			dirs = append(dirs, filepath.Join(base, "Google", "Cloud SDK", "google-cloud-sdk", "bin"))
		}
		if base := strings.TrimSpace(os.Getenv("ProgramFiles(x86)")); base != "" {
			dirs = append(dirs,
				filepath.Join(base, "Git", "cmd"),
				filepath.Join(base, "Git", "bin"),
				filepath.Join(base, "GitHub CLI"),
			)
			dirs = append(dirs, filepath.Join(base, "Google", "Cloud SDK", "google-cloud-sdk", "bin"))
		}
		dirs = append(dirs,
			filepath.Join("C:\\Program Files", "Git", "cmd"),
			filepath.Join("C:\\Program Files", "Git", "bin"),
			filepath.Join("C:\\Program Files", "GitHub CLI"),
			filepath.Join("C:\\Program Files (x86)", "Git", "cmd"),
			filepath.Join("C:\\Program Files (x86)", "Git", "bin"),
			filepath.Join("C:\\Program Files (x86)", "GitHub CLI"),
			filepath.Join("C:\\Program Files", "Google", "Cloud SDK", "google-cloud-sdk", "bin"),
			filepath.Join("C:\\Program Files (x86)", "Google", "Cloud SDK", "google-cloud-sdk", "bin"),
		)
		return dirs
	default:
		return nil
	}
}
