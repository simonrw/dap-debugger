package launch

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Vars holds what a configuration is resolved against: the values for VS
// Code's predefined ${...} variables, and the user's adapter and problem
// matcher definitions.
type Vars struct {
	Workspace string
	// File is the file open in the source view, used for ${file} and friends.
	File string
	Cwd  string
	Home string
	Env  func(string) string
	// Adapters are user adapter definitions keyed by configuration type.
	Adapters map[string]Adapter
	// Matchers are user background problem matchers for tasks.
	Matchers ProblemMatchers
}

var variable = regexp.MustCompile(`\$\{([^}]+)\}`)

// Substitute replaces variables in every string inside value.
func (v Vars) Substitute(value any) (any, error) {
	switch x := value.(type) {
	case string:
		return v.substituteString(x)
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			s, err := v.Substitute(item)
			if err != nil {
				return nil, err
			}
			out[i] = s
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			s, err := v.Substitute(item)
			if err != nil {
				return nil, err
			}
			out[k] = s
		}
		return out, nil
	case Config:
		m, err := v.Substitute(map[string]any(x))
		if err != nil {
			return nil, err
		}
		return Config(m.(map[string]any)), nil
	default:
		return value, nil
	}
}

func (v Vars) substituteString(s string) (string, error) {
	var firstErr error
	out := variable.ReplaceAllStringFunc(s, func(match string) string {
		value, err := v.lookup(match[2 : len(match)-1])
		if err != nil && firstErr == nil {
			firstErr = err
		}
		return value
	})
	return out, firstErr
}

func (v Vars) lookup(name string) (string, error) {
	if env, ok := strings.CutPrefix(name, "env:"); ok {
		if v.Env == nil {
			return os.Getenv(env), nil
		}
		return v.Env(env), nil
	}

	// Multi-root workspaces scope folders by name; there is only one folder here.
	if strings.HasPrefix(name, "workspaceFolder:") {
		name = "workspaceFolder"
	}

	switch name {
	case "workspaceFolder", "workspaceRoot":
		return v.Workspace, nil
	case "workspaceFolderBasename":
		return filepath.Base(v.Workspace), nil
	case "cwd":
		return v.Cwd, nil
	case "userHome":
		return v.Home, nil
	case "pathSeparator", "/":
		return string(filepath.Separator), nil
	}

	if strings.HasPrefix(name, "file") || strings.HasPrefix(name, "relativeFile") {
		return v.fileVariable(name)
	}

	return "", fmt.Errorf("%w ${%s}", ErrUnsupportedVariable, name)
}

func (v Vars) fileVariable(name string) (string, error) {
	if v.File == "" {
		return "", fmt.Errorf("${%s} needs an open file; open one in the source view first", name)
	}

	base := filepath.Base(v.File)
	ext := filepath.Ext(v.File)
	rel, err := filepath.Rel(v.Workspace, v.File)
	if err != nil {
		rel = v.File
	}

	switch name {
	case "file":
		return v.File, nil
	case "fileBasename":
		return base, nil
	case "fileBasenameNoExtension":
		return strings.TrimSuffix(base, ext), nil
	case "fileExtname":
		return ext, nil
	case "fileDirname":
		return filepath.Dir(v.File), nil
	case "fileDirnameBasename":
		return filepath.Base(filepath.Dir(v.File)), nil
	case "fileWorkspaceFolder":
		return v.Workspace, nil
	case "relativeFile":
		return rel, nil
	case "relativeFileDirname":
		return filepath.Dir(rel), nil
	}
	return "", fmt.Errorf("%w ${%s}", ErrUnsupportedVariable, name)
}
