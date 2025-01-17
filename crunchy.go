/*
 * crunchy - find common flaws in passwords
 *     Copyright (c) 2017-2018, Christian Muehlhaeuser <muesli@gmail.com>
 *
 *   For license see LICENSE
 */

package crunchy

import (
	"bufio"
	"encoding/hex"
	"hash"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/xrash/smetrics"
)

// Validator is used to setup a new password validator with options and dictionaries
type Validator struct {
	options     Options
	once        sync.Once
	wordsMaxLen int                 // length of longest word in dictionaries
	words       map[string]struct{} // map to index parsed dictionaries
	hashedWords map[string]string   // maps hash-sum to password
}

// Options contains all the settings for a Validator
type Options struct {
	// MinLength is the minimum length required for a valid password (>=1, default is 8)
	MinLength int
	// MinDiff is the minimum amount of unique characters required for a valid password (>=1, default is 5)
	MinDiff int
	// MinDist is the minimum WagnerFischer distance for mangled password dictionary lookups (>=0, default is 3)
	MinDist int
	// Hashers will be used to find hashed passwords in dictionaries
	Hashers []hash.Hash
	// DictionaryPath contains all the dictionaries that will be parsed (default is /usr/share/dict)
	DictionaryPath string
	// Check haveibeenpwned.com database
	CheckHIBP bool
	// MustContainDigit requires at least one digit for a valid password
	MustContainDigit bool
	// MustContainSymbol requires at least one special symbol for a valid password
	MustContainSymbol bool
}

// NewValidator returns a new password validator with default settings
func NewValidator() *Validator {
	return NewValidatorWithOpts(Options{
		MinDist:           -1,
		DictionaryPath:    "/usr/share/dict",
		CheckHIBP:         false,
		MustContainDigit:  false,
		MustContainSymbol: false,
	})
}

// NewValidatorWithOpts returns a new password validator with custom settings
func NewValidatorWithOpts(options Options) *Validator {
	if options.MinLength <= 0 {
		options.MinLength = 8
	}
	if options.MinDiff <= 0 {
		options.MinDiff = 5
	}
	if options.MinDist < 0 {
		options.MinDist = 3
	}

	return &Validator{
		options:     options,
		words:       make(map[string]struct{}),
		hashedWords: make(map[string]string),
	}
}

// indexDictionaries parses dictionaries/wordlists
func (v *Validator) indexDictionaries() {
	if v.options.DictionaryPath == "" {
		return
	}

	dicts, err := filepath.Glob(filepath.Join(v.options.DictionaryPath, "*"))
	if err != nil {
		return
	}

	for _, dict := range dicts {
		file, err := os.Open(dict)
		if err != nil {
			continue
		}

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			nw := normalize(scanner.Text())
			nwlen := len(nw)
			if nwlen > v.wordsMaxLen {
				v.wordsMaxLen = nwlen
			}

			// if a word is smaller than the minimum length minus the minimum distance
			// then any collisons would have been rejected by pre-dictionary checks
			if nwlen >= v.options.MinLength-v.options.MinDist {
				v.words[nw] = struct{}{}
			}

			for _, hasher := range v.options.Hashers {
				v.hashedWords[hashsum(nw, hasher)] = nw
			}
		}

		file.Close()
	}
}

// IndexDictionaries parses dictionaries/wordlists once
func (v *Validator) IndexDictionaries() {
	v.once.Do(v.indexDictionaries)
}

// foundInDictionaries returns whether a (mangled) string exists in the indexed dictionaries
func (v *Validator) foundInDictionaries(s string) error {
	v.IndexDictionaries()

	pw := normalize(s)   // normalized password
	revpw := reverse(pw) // reversed password
	pwlen := len(pw)

	// let's check perfect matches first
	// we can skip this if the pw is longer than the longest word in our dictionary
	if pwlen <= v.wordsMaxLen {
		if _, ok := v.words[pw]; ok {
			return &DictionaryError{ErrDictionary, pw, 0}
		}
		if _, ok := v.words[revpw]; ok {
			return &DictionaryError{ErrMangledDictionary, revpw, 0}
		}
	}

	// find hashed dictionary entries
	if pwindex, err := hex.DecodeString(pw); err == nil {
		if word, ok := v.hashedWords[string(pwindex)]; ok {
			return &HashedDictionaryError{ErrHashedDictionary, word}
		}
	}

	// find mangled / reversed passwords
	// we can skip this if the pw is longer than the longest word plus our minimum distance
	if pwlen <= v.wordsMaxLen+v.options.MinDist {
		for word := range v.words {
			if dist := smetrics.WagnerFischer(word, pw, 1, 1, 1); dist <= v.options.MinDist {
				return &DictionaryError{ErrMangledDictionary, word, dist}
			}
			if dist := smetrics.WagnerFischer(word, revpw, 1, 1, 1); dist <= v.options.MinDist {
				return &DictionaryError{ErrMangledDictionary, word, dist}
			}
		}
	}

	return nil
}

// Check validates a password for common flaws
// It returns nil if the password is considered acceptable.
func (v *Validator) Check(password string) error {
	if strings.TrimSpace(password) == "" {
		return ErrEmpty
	}
	if len(password) < v.options.MinLength {
		return ErrTooShort
	}
	if countUniqueChars(password) < v.options.MinDiff {
		return ErrTooFewChars
	}

	if v.options.MustContainDigit {
		validateDigit := regexp.MustCompile(`[0-9]+`)
		if !validateDigit.MatchString(password) {
			return ErrNoDigits
		}
	}

	if v.options.MustContainSymbol {
		validateSymbols := regexp.MustCompile(`[^\w\s]+`)
		if !validateSymbols.MatchString(password) {
			return ErrNoSymbols
		}
	}

	// Inspired by cracklib
	maxrepeat := 3.0 + (0.09 * float64(len(password)))
	if countSystematicChars(password) > int(maxrepeat) {
		return ErrTooSystematic
	}

	err := v.foundInDictionaries(password)
	if err != nil {
		return err
	}

	if v.options.CheckHIBP {
		err := foundInHIBP(password)
		if err != nil {
			return err
		}
	}

	return nil
}

// Rate grades a password's strength from 0 (weak) to 100 (strong).
func (v *Validator) Rate(password string) (uint, error) {
	if err := v.Check(password); err != nil {
		return 0, err
	}

	l := len(password)
	systematics := countSystematicChars(password)
	repeats := l - countUniqueChars(password)
	var letters, uLetters, numbers, symbols int

	for len(password) > 0 {
		r, size := utf8.DecodeRuneInString(password)
		password = password[size:]

		if unicode.IsLetter(r) {
			if unicode.IsUpper(r) {
				uLetters++
			} else {
				letters++
			}
		} else if unicode.IsNumber(r) {
			numbers++
		} else {
			symbols++
		}
	}

	// ADD: number of characters
	n := l * 4
	// ADD: uppercase letters
	if uLetters > 0 {
		n += (l - uLetters) * 2
	}
	// ADD: lowercase letters
	if letters > 0 {
		n += (l - letters) * 2
	}
	// ADD: numbers
	n += numbers * 4
	// ADD: symbols
	n += symbols * 6

	// REM: letters only
	if l == letters+uLetters {
		n -= letters + uLetters
	}
	// REM: numbers only
	if l == numbers {
		n -= numbers * 4
	}
	// REM: repeat characters (case insensitive)
	n -= repeats * 4
	// REM: systematic characters
	n -= systematics * 3

	if n < 0 {
		n = 0
	} else if n > 100 {
		n = 100
	}
	return uint(n), nil
}
