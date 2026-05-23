package vm

import "go.starlark.net/starlark"

// groupShell: run a command in the workspace sandbox. Requires a granted shell
// tool config; the call mutates the home dir, forcing an fs snapshot barrier.
var groupShell = []Builtin{
	{Name: "shell", Group: "shell", Doc: "shell(argv, dir='', env={}) -> {code,stdout,stderr}", Fn: bShell},
}

func bShell(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var argv *starlark.List
	var dir string
	var env *starlark.Dict
	if err := starlark.UnpackArgs("shell", args, kwargs, "argv", &argv, "dir?", &dir, "env?", &env); err != nil {
		return nil, err
	}
	gv, err := starlarkToGo(argv)
	if err != nil {
		return nil, err
	}
	e, err := dictToStringMap(env)
	if err != nil {
		return nil, err
	}
	return callCap(t, "shell", "exec", map[string]any{"argv": gv, "dir": dir, "env": e}, false, true)
}
