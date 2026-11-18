package edits

import (
	"fmt"
	"strings"
)

// Reply is what a protocol reads from one model answer. Content is the
// visible text only; reasoning is never parsed. A tool-calling protocol
// can add its calls here later and implement the same interface.
type Reply struct {
	Content string
}

// Protocol is how the model is told to propose edits and how they are read
// back. The block protocol is the only one today; tool calling can be
// added as another implementation without touching the workspace.
type Protocol interface {
	Name() string
	// Instructions is appended to the system prompt while edit mode is on.
	Instructions(r Rules) string
	// Extract returns the proposed edits of one reply.
	Extract(r Reply) ([]Block, []ParseError)
}

// Blocks is the text protocol: SEARCH/REPLACE, WHOLE and DELETE blocks in
// the reply, as plain text or inside code fences.
type Blocks struct{}

// Name implements Protocol.
func (Blocks) Name() string { return "blocks" }

// Extract implements Protocol.
func (Blocks) Extract(r Reply) ([]Block, []ParseError) { return Parse(r.Content) }

// Instructions implements Protocol.
func (Blocks) Instructions(r Rules) string {
	var b strings.Builder
	b.WriteString(`You can propose changes to the uploaded files. The user reviews every change before it is used. Paths are relative to the upload directory, exactly as in the file headers. Explain briefly, then write one block per change in this format, with each marker alone on its line:

To change part of an existing file, quote the current lines exactly, then the new lines:

FILE: path/to/file.go
<<<<<<< SEARCH
exact current lines
=======
new lines
>>>>>>> REPLACE

Several SEARCH/REPLACE blocks may follow one FILE line. Keep SEARCH short but unique in the file.
`)
	if r.AllowNew {
		b.WriteString(`
To create a file, or to rewrite a small file you were shown in full:

FILE: path/to/new.md
<<<<<<< WHOLE
complete file content
>>>>>>> WHOLE
`)
	} else {
		b.WriteString(`
To rewrite a small file you were shown in full:

FILE: path/to/file.md
<<<<<<< WHOLE
complete file content
>>>>>>> WHOLE

Creating new files is not allowed.
`)
	}
	if r.AllowDelete {
		b.WriteString(`
To delete a file:

FILE: path/to/old.txt
<<<<<<< DELETE
>>>>>>> DELETE
`)
	} else {
		b.WriteString("\nDeleting files is not allowed.\n")
	}
	b.WriteString(`
Never use WHOLE for a file marked as truncated; use SEARCH/REPLACE. Only text files can be changed. Do not write blocks when no change is asked for.`)
	return b.String()
}

// FailureMessage is the follow-up sent to the model for failed blocks. It
// quotes the real lines near each SEARCH that did not match. The message
// is capped at about maxBytes.
func FailureMessage(fails []Failure, maxBytes int) string {
	if len(fails) == 0 {
		return ""
	}
	if maxBytes <= 0 {
		maxBytes = 6000
	}
	var b strings.Builder
	b.WriteString("Some of your edit blocks could not be applied. Send corrected blocks for these only. Files that are not listed are fine.\n")
	for _, f := range fails {
		var item strings.Builder
		item.WriteString("\n- ")
		switch {
		case f.Path != "" && f.Kind == KindReplace && !f.Parse:
			fmt.Fprintf(&item, "FILE: %s, SEARCH block %d: %s", f.Path, f.Block, f.Reason)
		case f.Path != "" && !f.Parse:
			fmt.Fprintf(&item, "FILE: %s, %s block %d: %s", f.Path, f.Kind, f.Block, f.Reason)
		case f.Path != "":
			fmt.Fprintf(&item, "FILE: %s, reply line %d: %s", f.Path, f.Line, f.Reason)
		default:
			fmt.Fprintf(&item, "reply line %d: %s", f.Line, f.Reason)
		}
		item.WriteString("\n")
		if len(f.Near) > 0 {
			fmt.Fprintf(&item, "  Current lines %d-%d of the file:\n```\n%s\n```\n", f.NearAt, f.NearAt+len(f.Near)-1, strings.Join(f.Near, "\n"))
		}
		if b.Len()+item.Len() > maxBytes {
			b.WriteString("\n(more failures left out)\n")
			break
		}
		b.WriteString(item.String())
	}
	return b.String()
}
