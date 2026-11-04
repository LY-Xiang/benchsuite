package report

import (
	"encoding/xml"
	"os"

	"github.com/LY-Xiang/benchsuite/src/gate"
)

// The JUnit report a CI job consumes: one testcase per result, so a failing gate
// shows up as a failed test rather than as text somebody has to grep.

type failure struct {
	XMLName xml.Name `xml:"failure"`
	Message string   `xml:"message,attr"`
	Text    string   `xml:",chardata"`
}

type testCase struct {
	XMLName   xml.Name `xml:"testcase"`
	Name      string   `xml:"name,attr"`
	Classname string   `xml:"classname,attr"`
	Failure   *failure `xml:"failure,omitempty"`
}

type suite struct {
	XMLName xml.Name   `xml:"testsuite"`
	Name    string     `xml:"name,attr"`
	Tests   int        `xml:"tests,attr"`
	Cases   []testCase `xml:"testcase"`
}

// JUnit writes one testcase per result. The close error is returned when nothing
// earlier failed: a report that did not flush is not a report.
func JUnit(path string, results []gate.Result) (err error) {
	report := suite{Name: "bench", Tests: len(results)}
	for _, result := range results {
		entry := testCase{Name: result.Problem, Classname: result.Shape.ClassName()}
		if !result.Verdict.Passed() {
			// A timeout has no gate id -- it was killed, not refused -- so the message
			// is the bare word rather than "gate -": the old output read `gate` there
			// and a CI filter matching on it must keep working.
			message := "gate"
			if g := result.Gate.String(); g != string(gate.GateNone) {
				message = "gate " + g
			}
			entry.Failure = &failure{
				Message: message,
				Text:    result.Detail,
			}
		}
		report.Cases = append(report.Cases, entry)
	}

	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}()

	body, err := xml.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	_, err = file.WriteString(xml.Header + string(body) + "\n")
	return err
}
