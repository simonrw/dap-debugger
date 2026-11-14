// Package fzf ranks strings against a query using fzf's FuzzyMatchV2 scoring.
package fzf

import (
	"slices"
	"strings"
	"unicode"
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
	bonusBoundaryWhite       = bonusBoundary + 2
	bonusBoundaryDelimiter   = bonusBoundary + 1
)

type charClass int

// The order matters: everything after charNonWord can start a word.
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
	case strings.ContainsRune("/,:;|", r):
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
	if class > charNonWord {
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
		return bonusBoundaryWhite
	}
	return 0
}

// Filter returns the indices of the items matching query, best match first.
// The query is split on whitespace into terms that must all match. A term
// matches case-insensitively unless it contains an uppercase letter.
func Filter(query string, items []string) []int {
	var terms []term
	for _, field := range strings.Fields(query) {
		terms = append(terms, term{
			pattern:       []rune(field),
			caseSensitive: strings.ContainsFunc(field, unicode.IsUpper),
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
		index, score, length int
	}
	var matches []match

	var m matcher
items:
	for i, item := range items {
		text := []rune(item)
		total := 0

		for _, t := range terms {
			score, ok := m.score(text, t)
			if !ok {
				continue items
			}
			total += score
		}
		matches = append(matches, match{index: i, score: total, length: len(text)})
	}

	slices.SortStableFunc(matches, func(a, b match) int {
		if a.score != b.score {
			return b.score - a.score
		}
		return a.length - b.length
	})

	indices := make([]int, len(matches))
	for i, mt := range matches {
		indices[i] = mt.index
	}
	return indices
}

type term struct {
	pattern       []rune
	caseSensitive bool
}

// matcher holds the scratch buffers reused across items.
type matcher struct {
	text   []rune
	bonus  []int
	first  []int
	h, c   []int
	h1, c1 []int
}

func grow[T any](s []T, n int) []T {
	return slices.Grow(s[:0], n)[:n]
}

// score returns the best alignment score of the term within text, and false
// when the term's characters do not appear in order.
func (m *matcher) score(text []rune, t term) (int, bool) {
	pattern := t.pattern
	n, plen := len(text), len(pattern)
	if n < plen {
		return 0, false
	}

	m.text = grow(m.text, n)
	m.bonus = grow(m.bonus, n)
	m.first = grow(m.first, plen)
	m.h, m.c = grow(m.h, n), grow(m.c, n)
	m.h1, m.c1 = grow(m.h1, n), grow(m.c1, n)

	// First row of the score matrix, the per-character bonuses, and the
	// earliest position each pattern character can take.
	best := 0
	pidx := 0
	prevH := 0
	prevClass := charWhite
	inGap := false

	for i, r := range text {
		class := classOf(r)
		if !t.caseSensitive {
			r = unicode.ToLower(r)
		}
		m.text[i] = r

		bonus := bonusFor(prevClass, class)
		m.bonus[i] = bonus
		prevClass = class

		if pidx < plen && r == pattern[pidx] {
			m.first[pidx] = i
			pidx++
		}

		if r == pattern[0] {
			m.h[i] = scoreMatch + bonus*bonusFirstCharMultiplier
			m.c[i] = 1
			inGap = false

			if plen == 1 && m.h[i] > best {
				best = m.h[i]

				// fzf settles for the first match on a word boundary
				if bonus >= bonusBoundary {
					return best, true
				}
			}
		} else {
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

	if pidx != plen {
		return 0, false
	}

	// Remaining rows. h and c are the previous row, h1 and c1 the current
	// one; c counts the consecutive matched characters ending at a cell.
	for p := 1; p < plen; p++ {
		start := m.first[p]
		left := 0
		inGap = false

		for col := start; col < n; col++ {
			gapScore := left + scoreGapStart
			if inGap {
				gapScore = left + scoreGapExtension
			}

			matchScore, consecutive := 0, 0
			if pattern[p] == m.text[col] {
				matchScore = m.h[col-1] + scoreMatch
				bonus := m.bonus[col]
				consecutive = m.c[col-1] + 1

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

			m.h1[col] = left
			m.c1[col] = consecutive

			if p == plen-1 && left > best {
				best = left
			}
		}

		m.h, m.h1 = m.h1, m.h
		m.c, m.c1 = m.c1, m.c
	}

	return best, true
}
