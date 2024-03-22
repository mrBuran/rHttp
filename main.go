package main

import (
	"io"
	"log"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// Number of input or ENUM(input type, int)
const (
	host int = iota
	proto
	method
	urlPath

	header
	headerVal

	param
	paramVal

	cookie
	cookieVal

	form
	formVal

	// the last one is the max index of defined constants
	fieldsCount
)

const (
	hotPink       = lipgloss.Color("69")
	lightPink     = lipgloss.Color("225")
	darkGray      = lipgloss.Color("#767676")
	purple        = lipgloss.Color("141")
	brightPurple  = lipgloss.Color("183")
	brightPurple2 = lipgloss.Color("189")
	lightBlue     = lipgloss.Color("12")
	rose          = lipgloss.Color("177")
)

var (
	screenWidth  = 100
	screenHeight = 50
	offsetShift  = 5
	baseStyle    = lipgloss.NewStyle().Width(screenWidth)

	promptStyle       = lipgloss.NewStyle().Foreground(hotPink).Bold(true)
	promptActiveStyle = lipgloss.NewStyle().Foreground(rose).Bold(true)
	textStyle         = lipgloss.NewStyle().Foreground(purple)
	textValueStyle    = lipgloss.NewStyle().Foreground(brightPurple)
	continueStyle     = lipgloss.NewStyle().Foreground(darkGray)
	uriStyle          = lipgloss.NewStyle().Foreground(hotPink)
	headerStyle       = textStyle
	headerValueStyle  = lipgloss.NewStyle().Foreground(brightPurple)
	urlStyle          = lipgloss.NewStyle().Inherit(baseStyle).
				Foreground(brightPurple2).
				Bold(true).Padding(0, 1)

	bodyStyle  = lipgloss.NewStyle().Inherit(baseStyle).Foreground(lightPink)
	titleStyle = lipgloss.NewStyle().Foreground(lightBlue).
			Bold(true).BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("63")).
			Padding(1).Width(60).AlignHorizontal(lipgloss.Center)
)

func newReqest() (r *http.Request) {
	r, _ = http.NewRequest("GET", "http://localhost", nil)
	return
}

func sendRequest(r *http.Request) (*http.Response, error) {
	http_cli := http.Client{Timeout: 2 * time.Second}
	return http_cli.Do(r)
}

func correctHeader(i *textinput.Model) {
	// TODO use regexp instead
	h := i.Value()
	h = textproto.TrimString(h)
	h = strings.ReplaceAll(h, " ", "-")
	h = http.CanonicalHeaderKey(h)

	// auto correct header name
	i.SetValue(h)
}

func headersPrintf(h http.Header) []string {
	var order, lines []string
	for k := range h {
		order = append(order, k)
	}

	slices.Sort(order)

	// print headers
	for _, name := range order {
		val := strings.Join(h[name], ", ")
		nameRendered := headerStyle.Padding(0, 1).Render(name + ":")
		lines = append(
			lines, lipgloss.JoinHorizontal(
				lipgloss.Top,
				nameRendered,
				headerValueStyle.Padding(0, 1).
					Width(screenWidth-lipgloss.Width(nameRendered)).
					Render(val),
			),
		)
	}
	return lines
}

// The model is a state of app
type model struct {
	req          *http.Request
	res          *http.Response
	inputs       []textinput.Model
	cursorIdx    int    // edit type
	cursorKey    string // edit key of type orderedKeyVal store
	focused      int
	hideMenu     bool
	resBodyLines []string
	fullScreen   bool
	offset       int
	StatusBar
	KeyStroke
}

// Blur prompt.
func (m *model) blurPrompt(i int) {
	p := i
	switch i {
	case headerVal, paramVal, cookieVal, formVal:
		p = i - 1
	}
	m.inputs[p].PromptStyle = promptStyle
	m.inputs[i].Blur()
}

// Focus prompt.
func (m *model) focusPrompt(i int) {
	n := i
	switch i {
	case headerVal, paramVal, cookieVal, formVal:
		n = i - 1
	}
	m.inputs[n].PromptStyle = promptActiveStyle
	m.inputs[i].Focus()

}

// nextInput focuses the next input field
func (m *model) nextInput() {
	switch m.focused {
	case proto:
		m.setReqProto()
	case method:
		m.setReqMethod()
	}

	m.blurPrompt(m.focused)
	m.focused = (m.focused + 1) % len(m.inputs)
	m.focusPrompt(m.focused)
}

// prevInput focuses the previous input field
func (m *model) prevInput() {

	m.blurPrompt(m.focused)
	m.focused--
	// Wrap around
	if m.focused < 0 {
		m.focused = len(m.inputs) - 1
	}
	m.focusPrompt(m.focused)
}

// Request is executed.
func (m *model) reqIsExecuted() bool {
	return m.res != nil
}

// Clear response artefacts.
func (m *model) clearRespArtefacts() {
	m.res = nil
	m.resBodyLines = nil
	m.offset = 0
}

// Get page of response.
func (m *model) getRespPageLines() []string {
	limit := screenHeight - usedScreenLines - 2 // available screen lines for display of res body
	end := m.offset + limit
	if end > len(m.resBodyLines)-1 {
		return m.resBodyLines[m.offset:]
	}
	return m.resBodyLines[m.offset:end]
}

func (m *model) setReqHeader() {
	correctHeader(&m.inputs[header])
	name := m.inputs[header].Value()
	val := m.inputs[headerVal].Value()
	if name != "" && val != "" {
		m.req.Header.Set(name, val)
		m.inputs[header].Reset()
		m.inputs[headerVal].Reset()
	}
}

func (m *model) setReqParam() {
	v, _ := url.ParseQuery(m.req.URL.RawQuery)
	name := m.inputs[param].Value()
	val := m.inputs[paramVal].Value()
	if name != "" && val != "" {
		v.Set(name, val)
		m.req.URL.RawQuery = v.Encode()
		m.inputs[param].Reset()
		m.inputs[paramVal].Reset()
	}
}

func (m *model) setReqCookie() {
	name := m.inputs[cookie].Value()
	val := m.inputs[cookieVal].Value()
	if name != "" && val != "" {
		isNew := true
		for _, i := range m.req.Cookies() {
			if i.Name == name && i.Value == val {
				isNew = false
				break
			}
		}
		if isNew {
			m.req.AddCookie(&http.Cookie{Name: name, Value: val})
		}
		m.inputs[cookie].Reset()
		m.inputs[cookieVal].Reset()
	}
}

func (m *model) setReqForm() {
	name := m.inputs[form].Value()
	val := m.inputs[formVal].Value()
	if name != "" && val != "" {
		if formValues.Get(name) != "" {
			formValues.Set(name, val)
		} else {
			formValues.Add(name, val)
		}
		m.req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		m.req.Body = newReadCloser(formValues.Encode())
		m.inputs[form].Reset()
		m.inputs[formVal].Reset()
	}
}

func (m *model) setReqMethod() {
	r := regexp.MustCompile(`(?i)\b` + m.inputs[method].Value())
	if r.MatchString(strings.Join(allowedMethods, " ")) {
		m.inputs[method].SetValue(m.inputs[method].CurrentSuggestion())
		m.req.Method = m.inputs[method].Value()
		m.inputs[method].SetCursor(len(m.req.Method))
	} else { // not allowed HTTP method, reset to last saved
		m.inputs[method].SetValue(m.req.Method)
		m.inputs[method].CursorEnd()
	}
}

func (m *model) setReqProto() {
	val := m.inputs[proto].Value()
	if val != "0" && val != "1" {
		m.inputs[proto].SetValue("1")
		m.inputs[proto].CursorEnd()
	} else {
		m.req.Proto = "HTTP/1." + val
	}
}

func (m *model) setReqUrlPath() {
	val := m.inputs[urlPath].Value()
	m.req.URL.Path = val
}

func (m *model) setReqHost() {
	val := m.inputs[host].Value()
	m.req.URL.Host = val

}

func (m *model) restoreReqMethod() {
	m.inputs[method].SetValue(m.req.Method)
	m.inputs[method].CursorEnd()
}

func headerValidator(s string) error {
	// TODO add header validation
	// https://developers.cloudflare.com/rules/transform/request-header-modification/reference/header-format/
	// 	The name of the HTTP request header you want to set or remove can only contain:

	// Alphanumeric characters: a-z, A-Z, and 0-9
	// The following special characters: - and _
	// The value of the HTTP request header you want to set can only contain:

	// Alphanumeric characters: a-z, A-Z, and 0-9
	// The following special characters: _ :;.,\/"'?!(){}[]@<>=-+*#$&`|~^%
	return nil
}

var allowedMethods = []string{
	"GET", "POST", "PUT", "PATCH", "HEAD", "DELETE", "OPTIONS", "PROPFIND", "SEARCH",
	"TRACE", "PATCH", "PUT", "CONNECT",
}

var prompts = [fieldsCount]string{
	"Host   ", "HTTP/1.", "Method ", "Path  ",
	"Header ", "", "Param  ", "", "Cookie ", "", "Form   ", ""}
var placeholders = [fieldsCount]string{
	"example.com", "1", "GET", "/",
	"X-Auth-Token", "token value", "products_id", "10",
	"XDEBUG_SESSION", "debugger", "login", "user"}

func NewKeyValInputs(n int) textinput.Model {
	t := textinput.New()
	t.Prompt = prompts[n]
	t.Placeholder = placeholders[n]
	t.Width = 25
	t.PromptStyle = promptStyle
	t.PlaceholderStyle = continueStyle
	t.TextStyle = textValueStyle

	// set defaults input text
	switch n {
	case proto:
		t.SetValue("1")
		t.SetSuggestions([]string{"0", "1"})
		t.ShowSuggestions = true
		t.Width = 1
		t.CharLimit = 1
	case host:
		t.SetValue("localhost")
		t.Focus() // start program with first prompt activated
		t.PromptStyle = promptActiveStyle
	case method:
		t.SetValue("GET")
		t.SetSuggestions(allowedMethods)
		t.ShowSuggestions = true
	}
	return t
}

func initialModel() model {
	var inputs []textinput.Model

	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		panic(err)
	}
	screenWidth = w
	screenHeight = h

	for i := 0; i < fieldsCount; i++ {
		inputs = append(inputs, NewKeyValInputs(i))
	}
	return model{
		req:       newReqest(),
		inputs:    inputs,
		KeyStroke: NewKeyStroke(),
	}
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

var formValues = make(url.Values)

type readCloser struct {
	strings.Reader
}

func (rc *readCloser) Close() error      { return nil }
func newReadCloser(s string) *readCloser { return &readCloser{*strings.NewReader(s)} }

func eraseIfError(t textinput.Model) {
	if t.Err != nil {
		t.Reset()
	}
}

// Match lexer against given content type of http response.
func matchContentTypeTolexer(ct string) chroma.Lexer {
	for _, l := range lexers.GlobalLexerRegistry.Lexers {
		for _, mt := range l.Config().MimeTypes {
			if strings.Contains(ct, mt) {
				return l
			}
		}
	}

	return nil
}

func formatRespBody(ct, s string) []string {
	var content strings.Builder

	// huge one line splitter
	lp := lipgloss.NewStyle().Width(screenWidth).Padding(0, 1)
	s = lp.Render(s)

	lexer := matchContentTypeTolexer(ct)
	if lexer == nil {
		// detect lang
		lexer = lexers.Analyse(s)
	}
	lexer = chroma.Coalesce(lexer)

	// pick a style
	style := styles.Get("catppuccin-mocha")
	if style == nil {
		style = styles.Fallback
	}

	// pick a formatter
	formatter := formatters.Get("terminal16m")
	iterator, err := lexer.Tokenise(nil, s)
	if err != nil {
		// tea.Println(err)
		panic(err)
	}

	err = formatter.Format(&content, style, iterator)
	if err != nil {
		// tea.Println(err)
		panic(err)
	}

	return strings.Split(content.String(), "\n")
}

// Timer is a data container for some payload + time started.
type Timer struct {
	start   time.Time
	payload tea.Msg
}

// New message with timer.
func NewMessageWithTimer(payload any) Timer {
	return Timer{time.Now(), payload}
}

// Elapsed time from start of timer.
func (t *Timer) elapsedTime() time.Duration {
	return time.Since(t.start)
}

var usedScreenLines int

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd = make([]tea.Cmd, len(m.inputs))

	switch msg := msg.(type) {
	case Timer:
		m.reqTime = msg.elapsedTime()
		cmd := func() tea.Msg {
			return msg.payload
		}
		return m, cmd

	case *http.Response:
		defer msg.Body.Close()
		buf, _ := io.ReadAll(msg.Body)
		m.res = msg
		m.resBodyLines = formatRespBody(m.res.Header.Get("content-type"), string(buf))
		m.setStatus(statusInfo, "request is executed, response taken")

	case tea.WindowSizeMsg:
		m.setStatus(
			statusInfo,
			"detected screen size: "+strconv.Itoa(msg.Width)+" x "+strconv.Itoa(msg.Height))
		screenWidth = msg.Width
		screenHeight = msg.Height
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.PageDown):
			availableScreenLines := screenHeight - usedScreenLines - 2
			if m.offset+offsetShift+availableScreenLines <= len(m.resBodyLines) {
				m.offset += offsetShift
			} else {
				// decrease offset to take one last page in full size of screen lines
				m.offset += len(m.resBodyLines) - availableScreenLines - m.offset
				if m.offset < 0 {
					m.offset = 0
				}
			}
		case key.Matches(msg, m.keys.PageUp):
			if m.offset-offsetShift >= 0 {
				m.offset -= offsetShift
			} else {
				m.offset = 0
			}
		case key.Matches(msg, m.keys.FullScreen):
			if m.fullScreen {
				m.fullScreen = false
				return m, tea.ExitAltScreen
			}
			m.fullScreen = true
			return m, tea.EnterAltScreen
		case key.Matches(msg, m.keys.Run):
			m.setStatus(statusInfo, "sending request...")
			m.clearRespArtefacts()
			m.incReqCount()
			cmd := func() tea.Msg {
				r, err := sendRequest(m.req)
				if err != nil {
					return NewMessageWithTimer(err)
				}
				return NewMessageWithTimer(r)
			}
			return m, cmd
		case key.Matches(msg, m.keys.Prev):
			m.prevInput()
		case key.Matches(msg, m.keys.Next):
			m.nextInput()
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keys.Help):
			m.help.ShowAll = !m.help.ShowAll
			return m, nil
		case key.Matches(msg, m.keys.Enter):
			switch m.focused {
			case header, headerVal:
				m.setReqHeader()
			case param, paramVal:
				m.setReqParam()
			case cookie, cookieVal:
				m.setReqCookie()
			case form, formVal:
				m.setReqForm()
			case method:
				m.restoreReqMethod() // disallow changing the value by enter
			case urlPath:
				m.setReqUrlPath()
			case host:
				m.setReqHost()
			case proto:
				m.setReqProto()
			}

			// after handling enter is done, go to next input..
			m.nextInput()
		}

	case error:
		m.setStatus(statusError, msg.Error())
		m.clearRespArtefacts()
		return m, tea.ClearScreen
	}

	for i := 0; i < len(m.inputs); i++ {
		m.inputs[i], cmds[i] = m.inputs[i].Update(msg)
	}
	return m, tea.Batch(cmds...)
}

// Format status bar.
func (m *model) formatStatusBar() string {
	w := lipgloss.Width

	var resStatusCode int
	if m.reqIsExecuted() {
		resStatusCode = m.res.StatusCode
	}

	status := m.getStatusBadge("STATUS")
	reqCounter := reqCountStyle.Render(strconv.Itoa(m.getReqCount()))
	reqTime := reqTimeStyle.Render(m.getReqTime())

	indicator := indicatorStyle.Render(
		getStatusIndicator(resStatusCode, m.req.Proto))

	statusVal := statusText.Copy().
		Width(screenWidth - w(status) - w(reqCounter) - w(reqTime) - w(indicator)).
		Render(m.getStatusText())

	bar := lipgloss.JoinHorizontal(lipgloss.Top,
		status,
		statusVal,
		reqCounter,
		reqTime,
		indicator,
	)

	return statusBarStyle.Width(screenWidth).Render(bar)
}

func (m model) View() string {
	// Layout parts
	var (
		usedLines                                     int
		prompts, reqHeaders, resHeaders, resBodyLines []string
		reqUrl, resUrl, formValuesEncoded             string
	)

	// Text inputs
	for i := 0; i < fieldsCount; i += 2 {
		prompts = append(
			prompts,
			lipgloss.JoinHorizontal(lipgloss.Top, " ", m.inputs[i].View(), m.inputs[i+1].View()))
	}

	// Request URL
	reqUrl = urlStyle.Render(
		lipgloss.JoinHorizontal(lipgloss.Top, m.req.Proto, " ", m.req.Method, " ", m.req.URL.String()))

	// Request headers
	reqHeaders = headersPrintf(m.req.Header)

	// Request encoded form values
	if m.req.Body != nil {
		formValuesEncoded = " " + bodyStyle.Render(formValues.Encode())
	}

	// print response
	if m.reqIsExecuted() {

		// Response URL
		resUrl = urlStyle.Render(
			lipgloss.JoinHorizontal(lipgloss.Top, m.res.Proto, " ", m.res.Status))

		// Response headers
		resHeaders = headersPrintf(m.res.Header)

		// TODO..
		// if m.res.Header["Content-Type"] == "application/json" {
		// } else {
		// 	b.WriteString("\n" + string(m.resBody))
		// }
	}

	// add status bar
	statusBar := m.formatStatusBar()

	leftPanel := lipgloss.JoinVertical(lipgloss.Left, prompts...)
	lW, _ := lipgloss.Size(leftPanel)
	rW := screenWidth - lW
	if rW < 0 {
		rW = 0
	}
	m.help.Width = rW
	rightPanel := lipgloss.JoinVertical(lipgloss.Center,
		lipgloss.NewStyle().Width(rW).Render(m.help.View(m.keys)),
	)

	menu := lipgloss.JoinHorizontal(
		lipgloss.Top,
		leftPanel,
		rightPanel,
	)
	usedLines += len(prompts)

	reqInfo := []string{"", reqUrl}
	reqInfo = append(reqInfo, reqHeaders...)
	if formValuesEncoded != "" {
		reqInfo = append(reqInfo, "", formValuesEncoded)
	}
	reqInfoRendered := lipgloss.JoinVertical(lipgloss.Top, reqInfo...)
	usedLines += len(reqInfo)

	resInfo := []string{"", resUrl}
	resInfo = append(resInfo, resHeaders...)
	resInfo = append(resInfo, "")
	usedLines += len(resInfo)

	usedScreenLines = usedLines
	resBodyLines = m.getRespPageLines()
	resInfo = append(resInfo, resBodyLines...)
	resInfoRendered := lipgloss.JoinVertical(lipgloss.Top, resInfo...)

	// write all lines to output
	return lipgloss.JoinVertical(
		lipgloss.Top, menu, reqInfoRendered, resInfoRendered, statusBar,
	)
}

func main() {
	p := tea.NewProgram(initialModel())
	if _, err := p.Run(); err != nil {
		log.Fatal(err)
	}
}
