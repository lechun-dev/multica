package util

import (
	"regexp"
	"strings"
)

var commentMentionWithSpacing = regexp.MustCompile(`[\t ]*` + MentionRe.String() + `[\t ]*`)

// CommentEditMentions selects the current recipients who need an updated
// comment. Body edits notify everyone still mentioned; mention-only edits
// notify only newly added recipients. Issue links remain part of the body.
func CommentEditMentions(previousContent, content string) []Mention {
	var current []Mention
	for _, mention := range ParseMentions(content) {
		if mention.Type != "issue" {
			current = append(current, mention)
		}
	}
	if commentBodyWithoutMentions(previousContent) != commentBodyWithoutMentions(content) {
		return current
	}
	previous := make(map[Mention]bool)
	for _, mention := range ParseMentions(previousContent) {
		previous[mention] = true
	}
	var added []Mention
	for _, mention := range current {
		if !previous[mention] {
			added = append(added, mention)
		}
	}
	return added
}

func commentBodyWithoutMentions(content string) string {
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		removed := false
		body := commentMentionWithSpacing.ReplaceAllStringFunc(line, func(link string) string {
			if MentionRe.FindStringSubmatch(link)[2] == "issue" {
				return link
			}
			removed = true
			return " "
		})
		if removed {
			body = strings.TrimRight(body, " \t")
			if strings.TrimSpace(body) == "" {
				continue // Adding a mention-only line does not edit the body.
			}
		}
		lines = append(lines, body)
	}
	// Preserve body line breaks and internal whitespace: indentation in code
	// and paragraph boundaries can change the meaning of an edited comment.
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
