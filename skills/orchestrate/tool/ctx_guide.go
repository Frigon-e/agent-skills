package main

import "strings"

// How to write a concept query. It is shown where agents look: the top of
// `orch ctx find --help`, the home view, a tip under a weak `find` answer, the
// planner prompt and the docs. Queries agents type are never rewritten.

const ctxQueryGuide = "guide: When you do not know where code lives, search one behavior at a time in plain language:\n" +
	"  `Code that <action> on <entity>, when <trigger>, producing <result>`. Include only what you know.\n" +
	"  Do not invent symbols, frameworks or paths. Read the best hits, adopt the repo's own names, then use `sym` / `text` for exact lookups.\n" +
	"  Weak: `authentication`, `upload service controller queue`. Better: `Code that checks a user's credentials and establishes a signed-in session.`\n" +
	"  Read files with your native read tool (offset and limit for part of a file); orch ctx is for finding things, not for reading them; never use sed, cat, head or tail."

// conceptQueryRule is the sentence of the plan-arm planner prompt.
const conceptQueryRule = " A concept lookup's query must describe one behavior in the form `Code that <action> <entity>, when <trigger>, producing <result>`, with only what you know, and must not contain invented identifiers, frameworks or paths: use only names that appear in the spec or the dossier."

const conceptTipText = `tip: describe the behavior: "Code that <action> <entity> ..." (one behavior per query)`

var behaviorWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`shows checks creates sends stores handles returns lists validates builds starts restores loads saves reads writes updates deletes renders fetches parses computes calculates emits publishes finds gets sets adds removes retries signs issues authenticates uploads downloads converts resolves verifies applies calls triggers when that which whether establishes`) {
		behaviorWords[w] = true
	}
}

// conceptTip reports whether a concept answer should carry the tip: the query
// is a short noun list, or the answer is weak (at most one hit). A query that
// already starts "Code that" or has six or more words never does.
func conceptTip(query string, hits int) bool {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 || len(words) >= 6 || strings.HasPrefix(strings.Join(words, " "), "code that") {
		return false
	}
	if hits <= 1 {
		return true
	}
	if len(words) > 4 {
		return false
	}
	for _, w := range words {
		if behaviorWords[strings.Trim(w, ".,;:!?\"'`")] {
			return false
		}
	}
	return true
}
