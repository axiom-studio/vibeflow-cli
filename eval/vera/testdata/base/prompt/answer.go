package prompt

import (
	"fmt"
	"strings"
)

// Comment is one pull request comment.
type Comment struct {
	Author            string
	AuthorAssociation string // OWNER, MEMBER, COLLABORATOR, CONTRIBUTOR or NONE
	Body              string
}

// PullRequest is the pull request a question is about.
type PullRequest struct {
	Title             string
	Body              string
	Author            string
	AuthorAssociation string
	Comments          []Comment
}

// trusted reports whether text by an author with this association may reach
// a privileged prompt. Anyone can open a pull request or comment on a public
// repository.
func trusted(association string) bool {
	switch association {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	}
	return false
}

// AnswerPrompt builds the prompt for the project-context agent, which runs
// with the project's private documents and its VibeFlow tools. Only trusted
// text may enter it.
func AnswerPrompt(pr PullRequest, question string) string {
	var b strings.Builder
	b.WriteString("Answer the question using the project's private context.\n")
	for _, c := range pr.Comments {
		if !trusted(c.AuthorAssociation) {
			continue
		}
		fmt.Fprintf(&b, "Comment by %s:\n%s\n", c.Author, c.Body)
	}
	fmt.Fprintf(&b, "Question:\n%s\n", question)
	return b.String()
}
