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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/beevik/etree"
	"github.com/mitchellh/mapstructure"
)

// TODO add tests for loop+merge
// TODO don't include noResponseCode for constant sum CSV output
// TODO loop and merge doesn't appear to be working
// TODO rank order w/ radio buttons doesn't appear to be working

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

// Version of libsp
const Version = "0.2.1"
const timeFormat = "2006-01-02 15:04:05"
const noResponseConst = "No response"
const noResponseCode = "-99"
const noResponseCodeMulti = "0"
const notGrouped = "Not grouped"

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
		row := []string{r.ID, fmt.Sprintf("%t", r.Finished), fmt.Sprintf("%d", r.Progress), fmt.Sprintf("%d", r.Duration), fmt.Sprintf("%s", r.RecordedOn.Format(timeFormat))}

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

// csvCols returns a slice of string holding the column headers
func (s *Survey) csvCols() []string {
	cols := []string{"id", "finished", "progress", "duration", "recorded"}
	for _, id := range s.QuestionOrder {
		q := s.Questions[id]
		cols = append(cols, q.CSVCols()...)
	}
	return cols
}

// WriteR saves an R script suitable for importing the survey questions to R
func (s *Survey) WriteR(w *bufio.Writer, csvPath string) error {
	if w == nil {
		return errors.New("w cannot be nil")
	}

	scriptPreamble := `# Generated by sp ` + Version + ` (https://github.com/fflewddur/sp)
library(tidyverse)
`
	scriptDefs := "input_path <- \"" + csvPath + "\"\n"

	scriptImport := `message(sprintf("Reading %s...", input_path))
data <- read_csv(input_path, col_types = cols(
	finished = col_logical(),
	progress = col_integer(),
	duration = col_integer(),
	recorded = col_datetime(),
`

	choiceScales := make(map[string][]Choice)
	firstLine := true
	for _, id := range s.QuestionOrder {
		q := s.Questions[id]

		for _, colID := range q.CSVCols() {
			rColType, isRankCol := getColType(colID, q)
			if rColType != "" {
				if !firstLine {
					scriptImport += ",\n"
				} else {
					firstLine = false
				}
				if rColType == "col_factor()" {
					rColType = colTypeWithScales(q, isRankCol, choiceScales)
				}
				scriptImport += fmt.Sprintf("\t%s = %s", colID, rColType)
			}
		}
	}
	scriptImport += "\n))\n"

	scriptDefs += addScales(choiceScales)
	scriptCleanup := addCleanup(choiceScales)

	_, err := w.WriteString(scriptPreamble + "\n" + scriptDefs + "\n" + scriptImport + "\n" + scriptCleanup)
	if err != nil {
		return fmt.Errorf("could not write R script: %s", err)
	}
	err = w.Flush()
	if err != nil {
		return fmt.Errorf("could not flush R Writer: %s", err)
	}

	return nil
}

func getColType(colID string, q *Question) (rColType string, isRankCol bool) {
	isRankCol = false
	if q.qType == RankOrder {
		if strings.HasSuffix(colID, "_text") {
			rColType = ""
		} else {
			isRankCol = true
			rColType = "col_factor()"
		}
	} else if strings.HasSuffix(colID, "_text") {
		rColType = ""
	} else if strings.HasSuffix(colID, "_RANK") {
		isRankCol = true
		rColType = "col_factor()"
	} else if strings.HasSuffix(colID, "_first_click") ||
		strings.HasSuffix(colID, "_last_click") ||
		strings.HasSuffix(colID, "_page_submit") {
		rColType = "col_double()"
	} else if strings.HasSuffix(colID, "click_count") {
		rColType = "col_integer()"
	} else {
		rColType = q.RColType()
	}
	return
}

func colTypeWithScales(q *Question, isRankCol bool, choiceScales map[string][]Choice) string {
	var choices []Choice
	ordered := false
	if q.qType == PickGroupRank || q.qType == RankOrder {
		choices = make([]Choice, 0)
		if isRankCol {
			for i := 1; i <= len(q.ResponseChoices()); i++ {
				c := Choice{Label: fmt.Sprintf("%d", i)}
				choices = append(choices, c)
			}
			ordered = true
		} else {
			for _, g := range q.groups {
				c := Choice{Label: g}
				choices = append(choices, c)
			}
		}
	} else {
		choices = q.ResponseChoices()
		ordered = q.OrderedChoices()
	}

	rColType := "col_factor()"
	if len(choices) > 0 {
		if q.qType == PickGroupRank {
			choices = addNotGroupedOption(choices)
		}
		choices = addNoResponseOption(choices)
		scaleID := choiceScaleID(choices)
		if _, ok := choiceScales[scaleID]; !ok {
			choiceScales[scaleID] = choices
		}
		oString := ""
		if ordered {
			oString = ", ordered = TRUE"
		}
		rColType = "col_factor(levels = " + scaleID + oString + ")"
	}

	return rColType
}

func addNotGroupedOption(choices []Choice) []Choice {
	hasNotGrouped := false
	for _, c := range choices {
		if c.Label == notGrouped {
			hasNotGrouped = true
		}
	}
	if !hasNotGrouped {
		c := Choice{Label: notGrouped}
		choices = append(choices, c)
	}
	return choices
}

func addNoResponseOption(choices []Choice) []Choice {
	// TODO revisit the idea of "noResponse", maybe remove this logic
	hasNoResponse := false
	for _, c := range choices {
		if c.Label == noResponseConst {
			hasNoResponse = true
		}
	}
	if !hasNoResponse {
		c := Choice{Label: noResponseConst}
		choices = append(choices, c)
	}
	return choices
}

func choiceScaleID(choices []Choice) string {
	s := ""
	for _, c := range choices {
		s += c.Label
	}
	return fmt.Sprintf("scale_%x", sha1.Sum([]byte(s)))
}

func addScales(choiceScales map[string][]Choice) string {
	scales := []string{}
	for id, scale := range choiceScales {
		line := fmt.Sprintf("%s <- c(", id)
		firstLine := true
		for _, c := range scale {
			if !firstLine {
				line += ", "
			} else {
				firstLine = false
			}
			s := c.VarName
			if s == "" {
				s = c.Label
			}
			line += `"` + s + `"`
		}
		line += ")\n"
		scales = append(scales, line)
	}
	sort.Strings(scales)
	defs := ""
	for _, s := range scales {
		defs += s
	}
	return defs
}

func addCleanup(choiceScales map[string][]Choice) string {
	scales := []string{}
	for id := range choiceScales {
		scales = append(scales, id)
	}
	sort.Strings(scales)
	defs := "rm(input_path)\n"
	for _, s := range scales {
		defs += fmt.Sprintf("rm(%s)\n", s)
	}
	return defs
}

func isNoResponseCode(s string) bool {
	return s == noResponseCode || s == noResponseCodeMulti
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
		r.RecordedOn = getTimeElement("recordedDate", resp)

		for _, e := range resp.ChildElements() {
			r.AddAnswer(e.Tag, e.Text())
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

func getTimeElement(name string, e *etree.Element) time.Time {
	var retval time.Time
	if v := e.SelectElement(name); v != nil {
		var err error
		retval, err = time.Parse(timeFormat, v.Text())
		if err != nil {
			log.Printf("error converting '%s' to time: %s", v.Text(), err)
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
	embeddedDataIDs := []string{}
	// nQuestionsExpected := 0 // TODO Qualtrics' QC value doesn't seem to match # of questions...
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
				if f.ID != "" {
					s.blockOrder = append(s.blockOrder, f.ID)
				} else if f.Type == "EmbeddedData" {
					// Treat embedded data as survey questions
					for _, d := range f.EmbeddedData {
						q, err := newQuestionFromEmbeddedData(d)
						if err != nil {
							return fmt.Errorf("could not create question from JSON: %s", err)
						}
						s.Questions[q.ID] = q
						embeddedDataIDs = append(embeddedDataIDs, q.ID)
					}
				} else if f.Type == "BlockRandomizer" {
					for _, rf := range f.Flow {
						if rf.ID != "" {
							s.blockOrder = append(s.blockOrder, rf.ID)
						}
					}
				}
			}
		// case "QC":
		// 	var err error
		// 	nQuestionsExpected, err = strconv.Atoi(e.SecondaryAttribute)
		// 	if err != nil {
		// 		return fmt.Errorf("could not parse '%s' to int: %s", e.SecondaryAttribute, err)
		// 	}
		case "SQ":
			// TODO investigate N/A responses for loop-and-merge questions
			q, err := newQuestionFromPayload(e.Payload)
			if err != nil {
				return fmt.Errorf("could not create question from JSON: %s", err)
			}
			s.Questions[q.ID] = q
		}
	}

	// Qualtrics doesn't include embedded data in the question count.
	// They do include questions in the trash, so can't empty that yet.
	// nQuestions := len(s.Questions) - len(embeddedDataIDs)
	// if nQuestionsExpected != nQuestions {
	// 	return fmt.Errorf("expected %d questions but found %d", nQuestionsExpected, nQuestions)
	// }

	s.emptyTrash()
	s.sortQuestions()
	s.addDynamicChoices()
	s.addEmbeddedData(embeddedDataIDs)

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

// Run through all of the questions, pulling in dynamic choices from the appropriate questions
func (s *Survey) addDynamicChoices() {
	for _, qid := range s.QuestionOrder {
		q := s.Questions[qid]
		if q.dynChoices != nil {
			choiceSource := s.Questions[q.dynChoices.Source]
			if q.dynChoices.Type == "DisplayedChoices" || q.dynChoices.Type == "SelectedChoices" {
				if len(q.choices) == 0 {
					q.choices = make([]Choice, len(choiceSource.choices))
					copy(q.choices, choiceSource.choices)
					q.orderedChoices = choiceSource.orderedChoices
				}
				if len(q.subQuestions) == 0 {
					q.subQuestions = make([]Choice, len(choiceSource.subQuestions))
					copy(q.subQuestions, choiceSource.subQuestions)
				}
			}
			// TODO might need to support other types of dynamic choices
		}
	}
}

func (s *Survey) addEmbeddedData(ids []string) {
	for _, id := range ids {
		s.QuestionOrder = append(s.QuestionOrder, id)
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

var reElementType = regexp.MustCompile(`"Element"\s*:\s*"(.*?)"`)

func (e *qsfSurveyElement) UnmarshalJSON(b []byte) error {
	// Survey questions have a Payload object, other elements have an array of Payload objects.
	// Each Payload uses slightly different types, hence all of this logic.
	m := reElementType.FindSubmatch(b)
	if m == nil || len(m) <= 1 {
		return nil
	}

	element := string(m[1])
	switch element {
	case "SQ":
		reChoiceArray := regexp.MustCompile(`"Choices"\s*:\s*\[\s*{`)
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
				log.Printf("b: %s\n", b)
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
		} else {
			var q qsfSurveyElementQuestion
			err := json.Unmarshal(b, &q)
			if err != nil {
				log.Printf("b: %s\n", b)
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
			var blm qsfSurveyElementBlocksMap
			err := json.Unmarshal(b, &blm)
			if err != nil {
				log.Printf("b: %s\n", b)
				return fmt.Errorf("could not parse BL element: %s", err)
			}
			bl.Element = blm.Element
			for _, v := range blm.Payload {
				bl.Payload = append(bl.Payload, v)
			}
		}
		e.Element = bl.Element
		e.blocks = &bl
	case "FL":
		var fl qsfSurveyElementFlows
		err := json.Unmarshal(b, &fl)
		if err != nil {
			log.Printf("b: %s\n", b)
			return fmt.Errorf("could not parse FL element: %s", err)
		}
		e.Element = fl.Element
		e.flows = &fl
	case "QC":
		var data struct {
			Element            string
			PrimaryAttribute   string
			SecondaryAttribute string
		}
		err := json.Unmarshal(b, &data)
		if err != nil {
			log.Printf("b: %s\n", b)
			return fmt.Errorf("could not parse QC element: %s", err)
		}
		e.Element = data.Element
		e.PrimaryAttribute = data.PrimaryAttribute
		e.SecondaryAttribute = data.SecondaryAttribute
	}

	return nil
}

type qsfSurveyElementQuestion qsfSurveyElement

type qsfPayload struct {
	Type                       string
	QuestionText               string
	DataExportTag              string
	QuestionType               string
	QuestionDescription        string
	Selector                   string
	SubSelector                string
	QuestionID                 string
	ChoiceMap                  map[int]qsfChoice
	Choices                    interface{}
	ChoiceOrder                []interface{}
	DynamicChoices             *qsfDynChoices
	Answers                    map[int]qsfChoice
	AnswerOrder                []json.Number
	RecodeValues               map[int]interface{}
	VariableNaming             map[int]string
	ChoiceDataExportTags       interface{}
	HasChoiceDataExportTags    bool
	MappedChoiceDataExportTags map[int]string
	Groups                     []string
}

type qsfDynChoices struct {
	DynamicType string
	Locator     string
	Type        string
}

func (p *qsfPayload) OrderedChoices(choicesAreQuestions bool) ([]Choice, error) {
	ordered := []Choice{}

	for _, iface := range p.ChoiceOrder {
		// ChoiceOrder can be ints or strings, mixed in the same array. Thanks, Qualtrics.
		s := fmt.Sprintf("%v", iface)
		i, err := strconv.Atoi(s)
		if err != nil {
			log.Fatalf("could not convert '%s' to int: %s", s, err)
		}

		// If DynamicChoices is not nil, then Choices should be an empty array.
		// Otherwise, Choices should be a map[int]qsfChoice
		if p.DynamicChoices == nil {
			choiceMap := make(map[int]qsfChoice)
			if m, ok := p.Choices.(map[string]interface{}); ok {
				for k, v := range m {
					if key, err := strconv.Atoi(k); err == nil {
						var c qsfChoice
						if err := mapstructure.Decode(v, &c); err != nil {
							log.Printf("could not convert '%v' to qsfChoice: %s\n", v, err)
						}
						choiceMap[key] = c
					} else {
						log.Fatalf("could not convert '%v' to int: %s", k, err)
					}
				}
			}
			p.ChoiceMap = choiceMap
		}
		if _, ok := p.Choices.([]bool); ok {
			p.HasChoiceDataExportTags = false
		} else if m, ok := p.ChoiceDataExportTags.(map[string]interface{}); ok {
			p.MappedChoiceDataExportTags = make(map[int]string)
			p.HasChoiceDataExportTags = true
			for k, v := range m {
				if key, err := strconv.Atoi(k); err == nil {
					if val, ok := v.(string); ok {
						p.MappedChoiceDataExportTags[key] = val
					} else {
						log.Fatalf("could not convert '%v' to string", v)
					}
				} else {
					log.Fatalf("could not convert '%s' to int: %s", k, err)
				}
			}
		}

		hasText := false
		if len(p.ChoiceMap[i].TextEntry) > 0 {
			hasText, err = strconv.ParseBool(p.ChoiceMap[i].TextEntry)
			if err != nil {
				log.Fatalf("could not convert '%s' to bool: %s", p.ChoiceMap[i].TextEntry, err)
			}
		}

		// Do we have ChoiceDataExportTags? Qualtrics uses 'false' if there are not, and a map[int]string if there are.
		if _, ok := p.ChoiceDataExportTags.(bool); ok {
			p.HasChoiceDataExportTags = false
		} else if m, ok := p.ChoiceDataExportTags.(map[string]interface{}); ok {
			p.MappedChoiceDataExportTags = make(map[int]string)
			p.HasChoiceDataExportTags = true
			for k, v := range m {
				if key, err := strconv.Atoi(k); err == nil {
					if val, ok := v.(string); ok {
						p.MappedChoiceDataExportTags[key] = val
					} else {
						log.Fatalf("could not convert '%v' to string", v)
					}
				} else {
					log.Fatalf("could not convert '%s' to int: %s", k, err)
				}
			}
		}

		var varName string
		if choicesAreQuestions && p.HasChoiceDataExportTags {
			varName = p.MappedChoiceDataExportTags[i]
		} else {
			varName = p.VariableNaming[i]
		}

		c := Choice{ID: s, Label: p.ChoiceMap[i].Display, VarName: varName, HasText: hasText}
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

		c := Choice{ID: s.String(), Label: p.Answers[i].Display, VarName: p.VariableNaming[i], HasText: hasText}
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

type qsfSurveyElementBlocksMap struct {
	Element string
	Payload map[string]*qsfSurveyElementBlock
}

type qsfSurveyElementBlock struct {
	Type          string
	ID            string
	BlockElements []*qsfPayload
	Options       *qsfPayloadOptions
}

type qsfPayloadOptions struct {
	Looping string
}

type block struct {
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
	ID           string
	Type         string
	FlowID       string
	Flow         []*qsfSurveyElementFlow
	EmbeddedData []*qsfEmbeddedData
}

type qsfEmbeddedData struct {
	Type         string
	Field        string
	VariableType string
}
