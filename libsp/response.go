package libsp

import (
	"log"
	"regexp"
	"time"
)

// Response models a Qualtrics participant response
type Response struct {
	ID         string
	Progress   int
	Duration   int
	Finished   bool
	RecordedOn time.Time
	answers    map[string]string
}

// NewResponse creates and initializes a Response
func NewResponse() *Response {
	var r Response
	r.answers = make(map[string]string)
	return &r
}

var reQIDLoop = regexp.MustCompile(`^_\d+_(QID\d+.*)(-\d+)?`)
var reQIDDyn = regexp.MustCompile(`^(QID\d+_)x(\d+)(_TEXT)?$`)
var reTimer = regexp.MustCompile(`_(CLICK|SUBMIT|COUNT)$`)

// AddAnswer adds a question answer to the response
func (r *Response) AddAnswer(id string, answer string) {
	// Remove the extraneous characters in loop+merge response IDs
	// TODO this probably doesn't work for all possible uses of loop+merge
	matches := reQIDLoop.FindStringSubmatch(id)
	if matches != nil {
		timerMatches := reTimer.MatchString(matches[1])
		if !timerMatches {
			// Don't merge all of the timer responses
			id = matches[1]
		}
	} else {
		// Remove the 'x' character from dynamic response choices
		matches = reQIDDyn.FindStringSubmatch(id)
		if matches != nil {
			id = matches[1] + matches[2] + matches[3]
		}
	}
	if r.answers[id] != "" && answer != "" {
		log.Fatalf("error adding '%s' response for question '%s': already have '%s'", answer, id, r.answers[id])
	}

	r.answers[id] = answer
}
