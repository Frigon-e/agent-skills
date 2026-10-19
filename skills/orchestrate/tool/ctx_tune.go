package main

import "os"

// ctxTuning is the part of the `orch ctx` output that a gate experiment can
// vary (gate --ctx-profile, passed to the agents as ORCH_CTX_PROFILE). The
// default profile is the shipped behaviour.
type ctxTuning struct {
	Context    int  // lines around a text hit
	FuncLines  int  // lines of a function definition
	LeanConst  bool // a const or var shows its declaration, or its function's first lines when it sits in one
	SlimFooter bool // a page footer says only what the header does not: more, narrow, left out
}

var ctxProfiles = map[string]ctxTuning{
	"default": {Context: ctxContextDefault, FuncLines: defFuncLines},
	// lean: less text per hit and per definition, and a footer without the count the header already gives.
	"lean": {Context: 3, FuncLines: 20, LeanConst: true, SlimFooter: true},
}

// ctxProfileNames lists the profiles for flag help.
const ctxProfileNames = "default or lean"

// ctxTune is read once; an unknown name falls back to the default profile.
var ctxTune = func() ctxTuning {
	if t, ok := ctxProfiles[os.Getenv("ORCH_CTX_PROFILE")]; ok {
		return t
	}
	return ctxProfiles["default"]
}()

// profileArg is the profile a pass is told about; the default is left unset so
// the environment stays as it was.
func profileArg(name string) string {
	if name == "default" {
		return ""
	}
	return name
}
