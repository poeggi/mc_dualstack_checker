// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

const (
	// Real descriptions nest a few levels; each level parses its subtree
	// again, so deep nesting would cost depth times size.
	maxChatDepth = 16
	// Text built from a description stops growing here; answers keep far
	// less.
	chatTextMax = 32 << 10
)

// chatComponent is a parsed text component: its own text and style, then
// its children, which inherit the style.
type chatComponent struct {
	text tainted
	// A plain string has no style of its own and keeps the codes in its
	// text.
	plain bool
	style chatStyle
	extra []*chatComponent
}

// parseChat reads a text component: a string, an object, or an array whose
// first element is the parent of the others. Components nested deeper than
// maxChatDepth are left out, and so are fields of the wrong type. A
// component without text of its own, such as translate or object, shows its
// fallback, else nothing.
func parseChat(raw json.RawMessage, depth int) *chatComponent {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || depth > maxChatDepth {
		return nil
	}
	switch raw[0] {
	case '"':
		var s tainted
		if json.Unmarshal(raw, &s) != nil {
			return nil
		}
		return &chatComponent{text: s, plain: true}
	case '[':
		var list []json.RawMessage
		if json.Unmarshal(raw, &list) != nil || len(list) == 0 {
			return nil
		}
		c := parseChat(list[0], depth+1)
		if c != nil {
			c.extra = append(c.extra, parseChildren(list[1:], depth)...)
		}
		return c
	case '{':
		var obj struct {
			Text          tainted           `json:"text"`
			Translate     string            `json:"translate"`
			Fallback      tainted           `json:"fallback"`
			Extra         []json.RawMessage `json:"extra"`
			Color         string            `json:"color"`
			Bold          *bool             `json:"bold"`
			Italic        *bool             `json:"italic"`
			Underlined    *bool             `json:"underlined"`
			Strikethrough *bool             `json:"strikethrough"`
			Obfuscated    *bool             `json:"obfuscated"`
		}
		var typeErr *json.UnmarshalTypeError
		if err := json.Unmarshal(raw, &obj); err != nil && !errors.As(err, &typeErr) {
			return nil
		}
		c := &chatComponent{
			text: obj.Text,
			style: chatStyle{color: obj.Color, bold: obj.Bold, italic: obj.Italic, underlined: obj.Underlined,
				strikethrough: obj.Strikethrough, obfuscated: obj.Obfuscated},
			extra: parseChildren(obj.Extra, depth),
		}
		if c.text == "" && obj.Translate != "" {
			c.text = obj.Fallback
		}
		return c
	}
	return nil
}

// parseChildren reads the children of a component at depth.
func parseChildren(list []json.RawMessage, depth int) []*chatComponent {
	var out []*chatComponent
	for _, e := range list {
		if c := parseChat(e, depth+1); c != nil {
			out = append(out, c)
		}
	}
	return out
}

// visible is the text of c and its children, codes in plain strings kept.
func (c *chatComponent) visible() tainted {
	var b strings.Builder
	c.writeVisible(&b)
	return tainted(b.String())
}

func (c *chatComponent) writeVisible(b *strings.Builder) {
	if c == nil || b.Len() > chatTextMax {
		return
	}
	b.WriteString(string(c.text))
	for _, e := range c.extra {
		e.writeVisible(b)
	}
}

// legacy is c as text with legacy colour codes, the form Bedrock servers and
// older Java servers send. A hex colour becomes the sequence x followed by
// six digit codes. Every component starts with a reset, so styles never
// leak into its siblings. A plain string at the top keeps its own codes;
// below, it takes the style of its parent.
func (c *chatComponent) legacy() tainted {
	var b strings.Builder
	c.writeLegacy(&b, chatStyle{}, true)
	return tainted(b.String())
}

func (c *chatComponent) writeLegacy(b *strings.Builder, parent chatStyle, top bool) {
	if c == nil || b.Len() > chatTextMax {
		return
	}
	st := parent.with(c.style)
	switch {
	case c.plain && (top || c.text == ""):
		b.WriteString(string(c.text))
	case c.text != "":
		b.WriteString(st.codes())
		b.WriteString(string(c.text))
	}
	for _, e := range c.extra {
		e.writeLegacy(b, st, false)
	}
}

// chatStyle is the formatting a chat component passes on to its children.
type chatStyle struct {
	color                                               string
	bold, italic, underlined, strikethrough, obfuscated *bool
}

// with is st changed by the style a component sets itself.
func (st chatStyle) with(own chatStyle) chatStyle {
	if own.color != "" {
		st.color = own.color
	}
	for _, f := range []struct{ own, into **bool }{
		{&own.bold, &st.bold}, {&own.italic, &st.italic}, {&own.underlined, &st.underlined},
		{&own.strikethrough, &st.strikethrough}, {&own.obfuscated, &st.obfuscated},
	} {
		if *f.own != nil {
			*f.into = *f.own
		}
	}
	return st
}

// Legacy codes of the named chat colours.
var chatColors = map[string]byte{
	"black": '0', "dark_blue": '1', "dark_green": '2', "dark_aqua": '3',
	"dark_red": '4', "dark_purple": '5', "gold": '6', "gray": '7',
	"dark_gray": '8', "blue": '9', "green": 'a', "aqua": 'b',
	"red": 'c', "light_purple": 'd', "yellow": 'e', "white": 'f',
}

// codes is the reset plus the legacy codes that set st.
func (st chatStyle) codes() string {
	const sect = "\u00a7"
	out := sect + "r"
	if c, ok := chatColors[st.color]; ok {
		out += sect + string(c)
	} else if len(st.color) == 7 && st.color[0] == '#' {
		if _, err := strconv.ParseUint(st.color[1:], 16, 32); err == nil {
			out += sect + "x"
			for _, d := range strings.ToLower(st.color[1:]) {
				out += sect + string(d)
			}
		}
	}
	for _, f := range []struct {
		on   *bool
		code string
	}{{st.bold, "l"}, {st.italic, "o"}, {st.underlined, "n"}, {st.strikethrough, "m"}, {st.obfuscated, "k"}} {
		if f.on != nil && *f.on {
			out += sect + f.code
		}
	}
	return out
}
