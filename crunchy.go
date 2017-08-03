package crunchy

import (
	"errors"
	"io/ioutil"
	"path/filepath"
	"strings"
	"sync"

	"github.com/xrash/smetrics"
)

var (
	// ErrEmpty gets returned when the password is empty or all whitespace
	ErrEmpty = errors.New("Password is empty or all whitespace")
	// ErrTooShort gets returned when the password is not long enough
	ErrTooShort = errors.New("Password is too short")
	// ErrTooFewChars gets returned when the password does not contain enough unique characters
	ErrTooFewChars = errors.New("Password does not contain enough different/unique characters")
	// ErrTooSystematic gets returned when the password is too systematic (e.g. 123456, abcdef)
	ErrTooSystematic = errors.New("Password is too systematic")
	// ErrDictionary gets returned when the password is found in a dictionary
	ErrDictionary = errors.New("Password is too common / from a dictionary")
	// ErrMangledDictionary gets returned when the password is mangled, but found in a dictionary
	ErrMangledDictionary = errors.New("Password is mangled, but too common / from a dictionary")
)

type Validator struct {
	// minDiff is the minimum amount of unique characters required for a valid password
	minDiff int // = 5
	// minDist is the minimum WagnerFischer distance for mangled password dictionary lookups
	minDist int // = 3
	// minLength is the minimum length required for a valid password
	minLength int // = 6
	// dictionaryPath contains all the dictionaries that will be parsed
	dictionaryPath string // = "/usr/share/dict"

	once  sync.Once
	words map[string]struct{}
}

func NewValidator() *Validator {
	return NewValidatorWithOpts(-1, -1, -1, "/usr/share/dict")
}

func NewValidatorWithOpts(minDiff, minDist, minLength int, dictionaryPath string) *Validator {
	if minDiff < 0 {
		minDiff = 5
	}
	if minDist < 0 {
		minDist = 3
	}
	if minLength < 0 {
		minLength = 8
	}

	return &Validator{
		minDiff:        minDiff,
		minDist:        minDist,
		minLength:      minLength,
		dictionaryPath: dictionaryPath,
		words:          make(map[string]struct{}),
	}
}

// countUniqueChars returns the amount of unique runes in a string
func countUniqueChars(s string) int {
	m := make(map[rune]struct{})

	for _, c := range s {
		if _, ok := m[c]; !ok {
			m[c] = struct{}{}
		}
	}

	return len(m)
}

// countSystematicChars returns how many runes in a string are part of a sequence ('abcdef', '654321')
func countSystematicChars(s string) int {
	var x int
	rs := []rune(s)

	for i, c := range rs {
		if i == 0 {
			continue
		}
		if c == rs[i-1]+1 || c == rs[i-1]-1 {
			x++
		}
	}

	return x
}

// reverse returns the reversed form of a string
func reverse(s string) string {
	rs := []rune(s)
	for i, j := 0, len(rs)-1; i < j; i, j = i+1, j-1 {
		rs[i], rs[j] = rs[j], rs[i]
	}
	return string(rs)
}

// normalize returns the trimmed and lowercase version of a string
func normalize(s string) string {
	return strings.TrimSpace(strings.ToLower(s))
}

// indexDictionaries parses dictionaries/wordlists
func (v *Validator) indexDictionaries() {
	if v.dictionaryPath == "" {
		return
	}

	dicts, err := filepath.Glob(filepath.Join(v.dictionaryPath, "*"))
	if err != nil {
		return
	}

	for _, dict := range dicts {
		buf, err := ioutil.ReadFile(dict)
		if err != nil {
			continue
		}

		for _, word := range strings.Split(string(buf), "\n") {
			v.words[normalize(word)] = struct{}{}
		}
	}
}

// foundInDictionaries returns whether a (mangled) string exists in the indexed dictionaries
func (v *Validator) foundInDictionaries(s string) (string, error) {
	v.once.Do(v.indexDictionaries)

	pw := normalize(s)     // normalized password
	revpw := reverse(pw)   // reversed password
	mindist := len(pw) / 2 // minimum distance
	if mindist > v.minDist {
		mindist = v.minDist
	}

	// let's check perfect matches first
	if _, ok := v.words[pw]; ok {
		return pw, ErrDictionary
	}
	if _, ok := v.words[revpw]; ok {
		return revpw, ErrMangledDictionary
	}

	// find mangled / reversed passwords
	for word := range v.words {
		if dist := smetrics.WagnerFischer(word, pw, 1, 1, 1); dist <= mindist {
			// fmt.Printf("%s is too similar to %s\n", pw, word)
			return word, ErrMangledDictionary
		}
		if dist := smetrics.WagnerFischer(word, revpw, 1, 1, 1); dist <= mindist {
			// fmt.Printf("Reversed %s (%s) is too similar to %s: %d\n", pw, revpw, word, dist)
			return word, ErrMangledDictionary
		}
	}

	return "", nil
}

// ValidatePassword checks password for common flaws
// It returns nil if the password is considered acceptable.
func (v *Validator) Check(password string) error {
	if strings.TrimSpace(password) == "" {
		return ErrEmpty
	}
	if len(password) < v.minLength {
		return ErrTooShort
	}
	if countUniqueChars(password) < v.minDiff {
		return ErrTooFewChars
	}

	// Inspired by cracklib
	maxrepeat := 3.0 + (0.09 * float64(len(password)))
	if countSystematicChars(password) > int(maxrepeat) {
		return ErrTooSystematic
	}

	if _, err := v.foundInDictionaries(password); err != nil {
		return err
	}

	return nil
}
