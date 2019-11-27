package libsp

import (
	"bufio"
	"crypto/sha1"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/beevik/etree"
)

// Survey represents a survey, including its questions, potential responses, and meta-data
type Survey struct {
	Title         string
	Description   string
	Status        string
	CreatedOn     time.Time
	LaunchedOn    time.Time
	ModifiedOn    time.Time
	QuestionOrder []string
	Questions     map[string]*Question
	Responses     []*Response
	blocks        map[string]*block
	blockOrder    []string
}

const timeFormat = "2006-01-02 15:04:05"

// WriteCSV saves the parsed survey questions and responses in comma-separated value format
func (s *Survey) WriteCSV(bw *bufio.Writer) error {
	if bw == nil {
		return errors.New("bw cannot be nil")
	}

	w := csv.NewWriter(bw)
	err := w.Write(s.csvCols())
	if err != nil {
		return fmt.Errorf("could not write CSV columns: %s", err)
	}
	for _, r := range s.Responses {
		row := []string{r.ID, fmt.Sprintf("%t", r.Finished), fmt.Sprintf("%d", r.Progress), fmt.Sprintf("%d", r.Duration)}

		for _, id := range s.QuestionOrder {
			q := s.Questions[id]
			row = append(row, q.ResponseCols(r)...)
		}
		err = w.Write(row)
		if err != nil {
			return fmt.Errorf("could not write CSV row: %s", err)
		}
	}
	w.Flush()

	if err := w.Error(); err != nil {
		return fmt.Errorf("error writing csv: %s", err)
	}

	return nil
}

func (s *Survey) csvCols() []string {
	cols := []string{"id", "finished", "progress", "duration"}
	for _, id := range s.QuestionOrder {
		q := s.Questions[id]
		cols = append(cols, q.CSVCols()...)
	}
	return cols
}

// WriteR saves an R script suitable for importing the survey questions to R
func (s *Survey) WriteR(w *bufio.Writer) error {
	if w == nil {
		return errors.New("w cannot be nil")
	}

	_, err := w.WriteString(`# Generated by sp
library(readr)
input_path <- "test.csv"
message(sprintf("Reading %s...", input_path))
`)
	if err != nil {
		return fmt.Errorf("could not write R script: %s", err)
	}

	w.WriteString("data <- read_csv(input_path, col_types = cols(\n")

	// choiceScales := make(map[string][]Choice)
	firstLine := true
	for _, id := range s.QuestionOrder {
		q := s.Questions[id]

		for _, colID := range q.CSVCols() {
			var rColType string

			if strings.HasSuffix(colID, "_TEXT") {
				rColType = ""
			} else if strings.HasSuffix(colID, "_RANK") {
				rColType = "col_factor()"
			} else {
				rColType = q.RColType()
			}

			if rColType != "" {
				if !firstLine {
					w.WriteString(",\n")
				} else {
					firstLine = false
				}
				// if rColType == "col_factor()" {
				// 	q.ResponseChoices
				// 	choices := q.ResponseChoices()
				// 	scaleID := choiceScaleID(choices)
				// 	if _, ok := choiceScales[scaleID]; !ok {
				// 		choiceScales[scaleID] = choices
				// 	}
				// }
				w.WriteString(fmt.Sprintf("\t%s = %s", colID, rColType))
			}
		}
	}
	w.WriteString("\n))\n")

	err = w.Flush()
	if err != nil {
		return fmt.Errorf("could not flush R Writer: %s", err)
	}

	return nil
}

func choiceScaleID(choices []Choice) string {
	s := ""
	for _, c := range choices {
		s += c.Label
	}
	return fmt.Sprintf("%x", sha1.Sum([]byte(s)))
}

// ReadXML reads a Qualtrics XML file of participant responses
func (s *Survey) ReadXML(r *bufio.Reader) error {
	doc := etree.NewDocument()
	_, err := doc.ReadFrom(r)
	if err != nil {
		return fmt.Errorf("could not parse xml: %s", err)
	}
	responses := []*Response{}
	root := doc.SelectElement("Responses")
	for _, resp := range root.SelectElements("Response") {
		r := NewResponse()
		r.ID = getStringElement("_recordId", resp)
		r.Progress = getIntElement("progress", resp)
		r.Duration = getIntElement("duration", resp)
		r.Finished = getBoolElement("finished", resp)

		for _, e := range resp.ChildElements() {
			if strings.HasPrefix(e.Tag, "QID") {
				r.AddAnswer(e.Tag, e.Text())
			}
		}

		responses = append(responses, r)
	}
	s.Responses = responses
	return nil
}

func getStringElement(name string, e *etree.Element) string {
	var retval string
	if v := e.SelectElement(name); v != nil {
		retval = v.Text()
	}
	return retval
}

func getIntElement(name string, e *etree.Element) int {
	var retval int
	if v := e.SelectElement(name); v != nil {
		var err error
		retval, err = strconv.Atoi(v.Text())
		if err != nil {
			log.Printf("error converting '%s' to int: %s", v.Text(), err)
		}
	}
	return retval
}

func getBoolElement(name string, e *etree.Element) bool {
	var retval bool
	if v := e.SelectElement(name); v != nil {
		var err error
		retval, err = strconv.ParseBool(v.Text())
		if err != nil {
			log.Printf("error converting '%s' to bool: %s", v.Text(), err)
		}
	}
	return retval
}

// UnmarshalJSON fills the fields of s with the data found in b
func (s *Survey) UnmarshalJSON(b []byte) error {
	var qs qsf
	if err := json.Unmarshal(b, &qs); err != nil {
		return err
	}
	if qs.SurveyEntry == nil {
		return errors.New("json had no SurveyEntry object")
	}

	s.Title = qs.SurveyEntry.SurveyName
	s.Description = qs.SurveyEntry.SurveyDescription
	s.Status = qs.SurveyEntry.SurveyStatus
	if t, err := time.Parse(timeFormat, qs.SurveyEntry.SurveyCreationDate); err == nil {
		s.CreatedOn = t
	}
	if t, err := time.Parse(timeFormat, qs.SurveyEntry.SurveyStartDate); err == nil {
		s.LaunchedOn = t
	}
	if t, err := time.Parse(timeFormat, qs.SurveyEntry.LastModified); err == nil {
		s.ModifiedOn = t
	}

	s.Questions = make(map[string]*Question)
	s.blocks = make(map[string]*block)
	for _, e := range qs.SurveyElements {
		switch e.Element {
		case "BL":
			for _, p := range e.blocks.Payload {
				b := new(block)
				b.Type = p.Type
				b.ID = p.ID
				for _, be := range p.BlockElements {
					if be.Type == "Question" {
						b.QuestionIDs = append(b.QuestionIDs, be.QuestionID)
					}
				}
				s.blocks[b.ID] = b
			}
		case "FL":
			for _, f := range e.flows.Payload.Flow {
				s.blockOrder = append(s.blockOrder, f.ID)
			}
		case "QC":
			// TODO parse survey question count to verify we didn't miss any questions
		case "SQ":
			q, err := newQuestion(e.Payload)
			if err != nil {
				return fmt.Errorf("could not create question from JSON: %s", err)
			}
			s.Questions[q.ID] = q
		}
	}

	s.emptyTrash()
	s.sortQuestions()

	return nil
}

func (s *Survey) emptyTrash() {
	for _, b := range s.blocks {
		if b.Type == "Trash" {
			for _, id := range b.QuestionIDs {
				delete(s.Questions, id)
			}
		}
	}
}

func (s *Survey) sortQuestions() {
	s.QuestionOrder = []string{}
	for _, b := range s.blockOrder {
		s.QuestionOrder = append(s.QuestionOrder, s.blocks[b].QuestionIDs...)
	}
}

type qsf struct {
	SurveyEntry    *qsfSurveyEntry
	SurveyElements []*qsfSurveyElement
}

type qsfSurveyEntry struct {
	SurveyID           string
	SurveyName         string
	SurveyDescription  string
	SurveyStatus       string
	SurveyStartDate    string
	SurveyCreationDate string
	LastModified       string
}

type qsfSurveyElement struct {
	SurveyID           string
	Element            string
	PrimaryAttribute   string
	SecondaryAttribute string
	Payload            *qsfPayload
	blocks             *qsfSurveyElementBlocks
	flows              *qsfSurveyElementFlows
}

func (e *qsfSurveyElement) UnmarshalJSON(b []byte) error {
	// Survey questions have a Payload object, other elements have an array of Payload objects.
	// Each Payload uses slightly different types, hence all of this logic.
	reElementType := regexp.MustCompile(`"Element"\s*:\s*"(.*?)"`)
	m := reElementType.FindSubmatch(b)
	if m == nil || len(m) <= 1 {
		return nil
	}

	element := string(m[1])
	switch element {
	case "SQ":
		reChoiceArray := regexp.MustCompile(`"Choices"\s*:\s*\[\s*{`)
		reMetaInfo := regexp.MustCompile(`"QuestionText":\s*"Browser Meta Info"`)
		if reChoiceArray.Match(b) {
			// This Question has an array of Choice objects. I've only
			// seen this for NPS questions, in which case we don't care
			// about the Choice values because they must always be 0 - 10.
			var data struct {
				Element          string
				PrimaryAttribute string
				Payload          struct {
					QuestionText        string
					DataExportTag       string
					QuestionType        string
					QuestionDescription string
					Selector            string
					QuestionID          string
				}
			}
			err := json.Unmarshal(b, &data)
			if err != nil {
				return fmt.Errorf("could not parse SQ element: %s", err)
			}
			e.Element = data.Element
			e.PrimaryAttribute = data.PrimaryAttribute
			e.Payload = new(qsfPayload)
			e.Payload.QuestionText = data.Payload.QuestionText
			e.Payload.QuestionDescription = data.Payload.QuestionDescription
			e.Payload.DataExportTag = data.Payload.DataExportTag
			e.Payload.QuestionType = data.Payload.QuestionType
			e.Payload.Selector = data.Payload.Selector
			e.Payload.QuestionID = data.Payload.QuestionID
		} else if reMetaInfo.Match(b) {
			// This question uses a different JSON schema than the others.
			// For now, let's ignore it.
		} else {
			var q qsfSurveyElementQuestion
			err := json.Unmarshal(b, &q)
			if err != nil {
				fmt.Printf("b: %s\n", b)
				return fmt.Errorf("could not parse SQ element: %s", err)
			}
			e.Element = q.Element
			e.PrimaryAttribute = q.PrimaryAttribute
			e.SecondaryAttribute = q.SecondaryAttribute
			e.Payload = q.Payload
		}
	case "BL":
		var bl qsfSurveyElementBlocks
		err := json.Unmarshal(b, &bl)
		if err != nil {
			return fmt.Errorf("could not parse BL element: %s", err)
		}
		e.Element = bl.Element
		e.blocks = &bl
	case "FL":
		var fl qsfSurveyElementFlows
		err := json.Unmarshal(b, &fl)
		if err != nil {
			return fmt.Errorf("could not parse FL element: %s", err)
		}
		e.Element = fl.Element
		e.flows = &fl
	}

	return nil
}

type qsfSurveyElementQuestion qsfSurveyElement

type qsfPayload struct {
	Type                string
	QuestionText        string
	DataExportTag       string
	QuestionType        string
	QuestionDescription string
	Selector            string
	SubSelector         string
	QuestionID          string
	Choices             map[int]qsfChoice
	ChoiceOrder         []json.Number
	Answers             map[int]qsfChoice
	AnswerOrder         []json.Number
	RecodeValues        map[int]string
	VariableNaming      map[int]string
	Groups              []string
}

func (p *qsfPayload) OrderedChoices() ([]Choice, error) {
	ordered := []Choice{}
	for _, s := range p.ChoiceOrder {
		i64, err := s.Int64()
		if err != nil {
			log.Fatalf("could not convert '%s' to int: %s", s, err)
		}
		i := int(i64)

		hasText := false
		if len(p.Choices[i].TextEntry) > 0 {
			hasText, err = strconv.ParseBool(p.Choices[i].TextEntry)
			if err != nil {
				log.Fatalf("could not convert '%s' to bool: %s", p.Choices[i].TextEntry, err)
			}
		}

		c := Choice{ID: s.String(), Label: p.Choices[i].Display, HasText: hasText}
		ordered = append(ordered, c)
	}

	return ordered, nil
}

func (p *qsfPayload) OrderedAnswers() ([]Choice, error) {
	ordered := []Choice{}
	for _, s := range p.AnswerOrder {
		i64, err := s.Int64()
		if err != nil {
			log.Fatalf("could not convert '%s' to int: %s", s, err)
		}
		i := int(i64)

		hasText := false
		if len(p.Answers[i].TextEntry) > 0 {
			hasText, err = strconv.ParseBool(p.Answers[i].TextEntry)
			if err != nil {
				log.Fatalf("could not convert '%s' to bool: %s", p.Answers[i].TextEntry, err)
			}
		}

		c := Choice{ID: s.String(), Label: p.Answers[i].Display, HasText: hasText}
		ordered = append(ordered, c)
	}

	return ordered, nil
}

type qsfChoice struct {
	Display   string
	TextEntry string
}

type qsfSurveyElementBlocks struct {
	Element string
	Payload []*qsfSurveyElementBlock
}

type qsfSurveyElementBlock struct {
	Type          string
	ID            string
	BlockElements []*qsfPayload
}

type block struct {
	// TODO maybe make this an enum?
	Type        string
	ID          string
	QuestionIDs []string
}

type qsfSurveyElementFlows struct {
	Element string
	Payload *qsfSurveyElementFlowPayload
}

type qsfSurveyElementFlowPayload struct {
	Flow []*qsfSurveyElementFlow
}

type qsfSurveyElementFlow struct {
	ID     string
	Type   string
	FlowID string
}
