// Ported from fzf (https://github.com/junegunn/fzf, version 0.75, commit
// 4499615f): FuzzyMatchV2 and the path scheme from src/algo/algo.go, and the
// query handling and tie-breaks from src/pattern.go and src/result.go.
//
// Not ported:
//   - the extended query syntax (', ^, $, !, |)
//   - accent normalisation
//   - the FuzzyMatchV1 fallback for long inputs, so memory grows with item
//     length times query length; picker items are file paths, so this stays
//     small
//   - the 65535 caps on scores and lengths
//   - the backslash delimiter on Windows
//   - fzf splits the query on spaces, with a backslash escaping a space; this
//     splits on any whitespace
//
// fzf's licence:
//
// The MIT License (MIT)
//
// Copyright (c) 2013-2026 Junegunn Choi
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

// Package fzf ranks file paths against a query using fzf's FuzzyMatchV2
// scoring with its path scheme (--scheme=path).
package fzf

import (
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	scoreMatch        = 16
	scoreGapStart     = -3
	scoreGapExtension = -1

	bonusBoundary            = scoreMatch / 2
	bonusNonWord             = scoreMatch / 2
	bonusCamel123            = bonusBoundary + scoreGapExtension
	bonusConsecutive         = -(scoreGapStart + scoreGapExtension)
	bonusFirstCharMultiplier = 2
	bonusBoundaryWhite       = bonusBoundary
	bonusBoundaryDelimiter   = bonusBoundary + 1
)

type charClass int

// The order matters: charNonWord and every class after it can start a word.
const (
	charWhite charClass = iota
	charNonWord
	charDelimiter
	charLower
	charUpper
	charLetter
	charNumber
)

func classOf(r rune) charClass {
	switch {
	case r >= 'a' && r <= 'z':
		return charLower
	case r >= 'A' && r <= 'Z':
		return charUpper
	case r >= '0' && r <= '9':
		return charNumber
	case strings.ContainsRune(" \t\n\v\f\r", r):
		return charWhite
	case r == '/':
		return charDelimiter
	case r < unicode.MaxASCII:
		return charNonWord
	case unicode.IsLower(r):
		return charLower
	case unicode.IsUpper(r):
		return charUpper
	case unicode.IsNumber(r):
		return charNumber
	case unicode.IsLetter(r):
		return charLetter
	case unicode.IsSpace(r):
		return charWhite
	}
	return charNonWord
}

func bonusFor(prev, class charClass) int {
	if class >= charNonWord {
		switch prev {
		case charWhite:
			return bonusBoundaryWhite
		case charDelimiter:
			return bonusBoundaryDelimiter
		case charNonWord:
			return bonusBoundary
		}
	}

	if prev == charLower && class == charUpper || prev != charNumber && class == charNumber {
		return bonusCamel123
	}

	switch class {
	case charNonWord, charDelimiter:
		return bonusNonWord
	case charWhite:
		// Terms never contain whitespace, so this bonus never scores; kept to
		// match upstream.
		return bonusBoundaryWhite
	}
	return 0
}

// Filter returns the indices of the items matching query, best match first.
// The query is split on whitespace into terms that must all match. A term
// matches case-insensitively unless lowercasing would change it. Ties in
// score go to the item whose earliest term match starts closest after its
// last path separator, then to the shorter item (ignoring surrounding
// whitespace), then to the earlier item.
func Filter(query string, items []string) []int {
	var terms []term
	for _, field := range strings.Fields(query) {
		terms = append(terms, term{
			pattern:       []rune(field),
			caseSensitive: strings.ToLower(field) != field,
		})
	}

	if len(terms) == 0 {
		indices := make([]int, len(items))
		for i := range items {
			indices[i] = i
		}
		return indices
	}

	type match struct {
		index, score, pathname, length int
	}
	var matches []match

	var m matcher
	var text []rune
nextItem:
	for i, item := range items {
		text = text[:0]
		for _, r := range item {
			text = append(text, r)
		}
		total := 0
		begin := len(text)

		for _, t := range terms {
			score, start, ok := m.match(text, t)
			if !ok {
				continue nextItem
			}
			total += score
			begin = min(begin, start)
		}

		matches = append(matches, match{
			index:    i,
			score:    total,
			pathname: pathnameDistance(text, begin),
			length:   utf8.RuneCountInString(strings.TrimSpace(item)),
		})
	}

	slices.SortStableFunc(matches, func(a, b match) int {
		if a.score != b.score {
			return b.score - a.score
		}
		if a.pathname != b.pathname {
			return a.pathname - b.pathname
		}
		return a.length - b.length
	})

	indices := make([]int, len(matches))
	for i, mt := range matches {
		indices[i] = mt.index
	}
	return indices
}

// pathnameDistance returns how far past the last path separator a match
// starting at begin lies, or math.MaxInt when it starts before it.
func pathnameDistance(text []rune, begin int) int {
	last := -1
	for i, r := range slices.Backward(text) {
		// fzf counts a backslash as a separator here on every platform
		if r == '/' || r == '\\' {
			last = i
			break
		}
	}

	if last > begin {
		return math.MaxInt
	}
	return begin - last
}

type term struct {
	pattern       []rune
	caseSensitive bool
}

// in reports whether the term's characters appear in order in text.
func (t term) in(text []rune) bool {
	p := 0
	for _, r := range text {
		if !t.caseSensitive && classOf(r) == charUpper {
			r = unicode.ToLower(r)
		}
		if r == t.pattern[p] {
			p++
			if p == len(t.pattern) {
				return true
			}
		}
	}
	return false
}

// matcher holds the scratch buffers reused across items.
type matcher struct {
	// folded is the item's text, lowercased unless the term is case sensitive.
	folded []rune
	// bonus is the bonus for matching each character of the text.
	bonus []int
	// first is the earliest position each pattern character can match.
	first []int
	// h is the score matrix and c counts the consecutive matched characters
	// ending at each cell, one row of len(folded) cells per pattern character.
	h, c []int
}

func grow[T any](s []T, n int) []T {
	return slices.Grow(s[:0], n)[:n]
}

// match returns the best alignment score of the term within text and the
// position where that alignment starts, or false when the term's characters
// do not appear in order. Of equally scored alignments it picks the one
// ending furthest right.
func (m *matcher) match(text []rune, t term) (score, start int, ok bool) {
	pattern := t.pattern
	n, plen := len(text), len(pattern)
	// Most items do not match, and rejecting them here skips filling the matrix
	if n < plen || !t.in(text) {
		return 0, 0, false
	}

	m.folded = grow(m.folded, n)
	m.bonus = grow(m.bonus, n)
	m.first = grow(m.first, plen)
	m.h = grow(m.h, n*plen)
	m.c = grow(m.c, n*plen)

	// First row, the per-character bonuses, the earliest position each
	// pattern character can take, and the last position the final pattern
	// character takes.
	best, bestCol := 0, 0
	pidx, last := 0, 0
	prevH := 0
	prevClass := charDelimiter
	inGap := false

	for i, r := range text {
		class := classOf(r)
		if !t.caseSensitive && class == charUpper {
			r = unicode.ToLower(r)
		}
		m.folded[i] = r

		bonus := bonusFor(prevClass, class)
		m.bonus[i] = bonus
		prevClass = class

		if r == pattern[min(pidx, plen-1)] {
			if pidx < plen {
				m.first[pidx] = i
				pidx++
			}
			last = i
		}

		if r == pattern[0] {
			m.h[i] = scoreMatch + bonus*bonusFirstCharMultiplier
			m.c[i] = 1
			inGap = false

			if plen == 1 && m.h[i] >= best {
				best, bestCol = m.h[i], i
			}
		} else {
			// Carry an earlier first-character match's score rightwards, less
			// the gap penalties, so the next row can extend it from a later
			// column.
			penalty := scoreGapStart
			if inGap {
				penalty = scoreGapExtension
			}
			m.h[i] = max(prevH+penalty, 0)
			m.c[i] = 0
			inGap = true
		}
		prevH = m.h[i]
	}

	if plen == 1 {
		return best, bestCol, true
	}

	for p := 1; p < plen; p++ {
		row, above := p*n, (p-1)*n
		left := 0
		inGap = false

		for col := m.first[p]; col <= last; col++ {
			gapScore := left + scoreGapStart
			if inGap {
				gapScore = left + scoreGapExtension
			}

			matchScore, consecutive := 0, 0
			if pattern[p] == m.folded[col] {
				matchScore = m.h[above+col-1] + scoreMatch
				bonus := m.bonus[col]
				consecutive = m.c[above+col-1] + 1

				if consecutive > 1 {
					chunkBonus := m.bonus[col-consecutive+1]
					if bonus >= bonusBoundary && bonus > chunkBonus {
						// A word boundary starts a new chunk
						consecutive = 1
					} else {
						bonus = max(bonus, bonusConsecutive, chunkBonus)
					}
				}

				if matchScore+bonus < gapScore {
					matchScore += m.bonus[col]
					consecutive = 0
				} else {
					matchScore += bonus
				}
			}

			inGap = matchScore < gapScore
			left = max(matchScore, gapScore, 0)

			m.h[row+col] = left
			m.c[row+col] = consecutive

			if p == plen-1 && left >= best {
				best, bestCol = left, col
			}
		}
	}

	return best, m.start(plen, n, bestCol), true
}

// start walks the score matrix back from the best cell in the last row to the
// position the first pattern character matched, as fzf's backtrace does.
func (m *matcher) start(plen, n, col int) int {
	p := plen - 1
	preferMatch := true

	for {
		row := p * n
		score := m.h[row+col]

		diag, left := 0, 0
		if p > 0 && col >= m.first[p] {
			diag = m.h[row-n+col-1]
		}
		if col > m.first[p] {
			left = m.h[row+col-1]
		}

		current := p
		if score > diag && (score > left || score == left && preferMatch) {
			if p == 0 {
				return col
			}
			p--
		}

		// The next pattern character's row is only filled from its first
		// possible match, so cells left of that hold values from earlier
		// matches.
		preferMatch = m.c[row+col] > 1 ||
			current+1 < plen && col+1 >= m.first[current+1] && m.c[row+n+col+1] > 0
		col--
	}
}
