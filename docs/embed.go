// Package docs embeds the human-facing documentation that also ships
// inside the binary.
//
// toolbox.md is both the guide README.md links to and the reference file
// coop writes into the injected plugin (see internal/skills). Embedding
// the one file rather than restating its rules in the skill is what stops
// the two drifting: the reserved-name set and the all-in/all-out rule
// have a single source, and a doc edit reaches the skill by rebuilding.
package docs

import _ "embed"

// Toolbox is docs/toolbox.md, the coop:init skill's reference file.
//
//go:embed toolbox.md
var Toolbox string
