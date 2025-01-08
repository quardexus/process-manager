package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// -----------------------------------------------------------------------------
// Configuration
// -----------------------------------------------------------------------------

type Config struct {
	Processes []string `json:"processes"`
}

// -----------------------------------------------------------------------------
// Process data
// -----------------------------------------------------------------------------

type ProcessItem struct {
	CmdLine  string
	Logs     []string
	Cmd      *exec.Cmd
	Finished bool

	stdout io.ReadCloser
	stderr io.ReadCloser
	mu     sync.Mutex
}

type processListItem struct {
	p *ProcessItem
}

func (it processListItem) Title() string       { return it.p.CmdLine }
func (it processListItem) Description() string { return "" }
func (it processListItem) FilterValue() string { return it.p.CmdLine }

type newProcessItem struct{}

func (ni newProcessItem) Title() string       { return "[Create a new process]" }
func (ni newProcessItem) Description() string { return "" }
func (ni newProcessItem) FilterValue() string { return "" }

// -----------------------------------------------------------------------------
// States
// -----------------------------------------------------------------------------

type state int

const (
	stateList state = iota
	stateAddProcess
	stateViewLogs
)

// -----------------------------------------------------------------------------
// Log update message
// -----------------------------------------------------------------------------

type logUpdateMsg struct {
	processIndex int
}

// -----------------------------------------------------------------------------
// Main Bubble Tea model
// -----------------------------------------------------------------------------

type model struct {
	state               state
	list                list.Model
	textInput           textinput.Model
	viewport            viewport.Model
	processes           []*ProcessItem
	currentProcessIndex int
	configPath          string

	justEnteredLogs bool
	program         *tea.Program

	// We store current window size
	windowWidth  int
	windowHeight int
}

// -----------------------------------------------------------------------------
// We'll keep up to maxLogLines lines per process
// -----------------------------------------------------------------------------

const maxLogLines = 1000

// -----------------------------------------------------------------------------
// Periodic tick
// -----------------------------------------------------------------------------

type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(time.Millisecond*300, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// -----------------------------------------------------------------------------
// Load/save config
// -----------------------------------------------------------------------------

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func saveConfig(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func createDefaultConfig(path string) (*Config, error) {
	cfg := &Config{Processes: []string{}}
	if err := saveConfig(path, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// -----------------------------------------------------------------------------
// Helper function to append a log line with limit
// -----------------------------------------------------------------------------

func appendLog(p *ProcessItem, line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Logs = append(p.Logs, line)
	if len(p.Logs) > maxLogLines {
		p.Logs = p.Logs[len(p.Logs)-maxLogLines:]
	}
}

// -----------------------------------------------------------------------------
// Start process
// -----------------------------------------------------------------------------

func startProcess(program *tea.Program, processIndex int, cmdLine string) (*ProcessItem, error) {
	parts := strings.Fields(cmdLine)
	if len(parts) == 0 {
		return nil, errors.New("empty command line")
	}

	cmd := exec.Command(parts[0], parts[1:]...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	pItem := &ProcessItem{
		CmdLine: cmdLine,
		Cmd:     cmd,
		stdout:  stdout,
		stderr:  stderr,
		Logs:    []string{},
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	go pItem.readOutput(program, processIndex, stdout)
	go pItem.readOutput(program, processIndex, stderr)

	go func() {
		_ = cmd.Wait()
		pItem.mu.Lock()
		pItem.Finished = true
		pItem.mu.Unlock()

		appendLog(pItem, "[Process finished]")
	}()

	return pItem, nil
}

// -----------------------------------------------------------------------------
// Reading output (limit logs to maxLogLines)
// -----------------------------------------------------------------------------

func (p *ProcessItem) readOutput(program *tea.Program, processIndex int, r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()

		utf8Line, err := cp866ToUTF8(line)
		if err != nil {
			utf8Line = line
		}

		appendLog(p, utf8Line)

		if program != nil {
			program.Send(logUpdateMsg{processIndex: processIndex})
		}
	}
}

// -----------------------------------------------------------------------------
// Initialize model
// -----------------------------------------------------------------------------

func initialModel(program *tea.Program) model {
	exePath, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	dir := filepath.Dir(exePath)
	configPath := filepath.Join(dir, "process_manager_config.json")

	cfg, err := loadConfig(configPath)
	if os.IsNotExist(err) {
		cfg, err = createDefaultConfig(configPath)
		if err != nil {
			log.Fatal("Failed to create config:", err)
		}
	} else if err != nil {
		log.Fatal("Failed to load config:", err)
	}

	var processes []*ProcessItem
	for i, cmdLine := range cfg.Processes {
		pItem, err := startProcess(nil, i, cmdLine)
		if err != nil {
			log.Printf("Error starting '%s': %v\n", cmdLine, err)
			continue
		}
		processes = append(processes, pItem)
	}

	var items []list.Item
	for _, pr := range processes {
		items = append(items, processListItem{p: pr})
	}
	items = append(items, newProcessItem{})

	l := list.New(items, list.NewDefaultDelegate(), 50, 12)
	l.Title = "Quardexus Process Manager v0.1"
	l.SetShowHelp(true)
	l.SetShowStatusBar(false)
	l.SetShowFilter(true)
	l.SetShowPagination(true)

	ti := textinput.New()
	ti.Placeholder = "Enter a command..."
	ti.Focus()

	vp := viewport.New(0, 0)
	vp.Style = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240"))
	vp.MouseWheelEnabled = true

	return model{
		state:               stateList,
		list:                l,
		textInput:           ti,
		viewport:            vp,
		processes:           processes,
		currentProcessIndex: -1,
		configPath:          configPath,
		justEnteredLogs:     false,
		program:             program,
		windowWidth:         0,
		windowHeight:        0,
	}
}

// -----------------------------------------------------------------------------
// Update
// -----------------------------------------------------------------------------

func (m model) Init() tea.Cmd {
	return tea.Batch(
		tickCmd(),
	)
}

// killAllProcesses kills all unfinished processes before quitting
func (m model) killAllProcesses() {
	for _, pItem := range m.processes {
		if !pItem.Finished && pItem.Cmd != nil && pItem.Cmd.Process != nil {
			_ = pItem.Cmd.Process.Kill()
			appendLog(pItem, "[Process killed by exit]")
		}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.state {

	// -----------------------------------------------------------------
	//  stateList
	// -----------------------------------------------------------------
	case stateList:
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "ctrl+c", "q":
				m.killAllProcesses()
				return m, tea.Quit

			case "enter":
				idx := m.list.Index()
				itm := m.list.Items()[idx]

				switch itm.(type) {
				case newProcessItem:
					m.state = stateAddProcess
					m.textInput.SetValue("")
					return m, nil

				case processListItem:
					m.currentProcessIndex = idx
					m.state = stateViewLogs
					m.justEnteredLogs = true

					if m.windowWidth > 0 && m.windowHeight > 0 {
						m.viewport.Width = m.windowWidth
						m.viewport.Height = m.windowHeight - 5
					}

					p := m.processes[m.currentProcessIndex]
					now := time.Now().Format("2006-01-02 15:04:05")
					appendLog(p, fmt.Sprintf("%s [Logs window opened: %s]", now, p.CmdLine))

					m = m.updateLogsViewport(true)
					return m, nil
				}
			}

		case tickMsg:
			return m, tickCmd()

		case tea.WindowSizeMsg:
			m.windowWidth = msg.Width
			m.windowHeight = msg.Height
			// Dynamically adjust list height based on window size
			newHeight := msg.Height - 5
			if newHeight < 3 {
				newHeight = 3
			}
			m.list.SetHeight(newHeight)
			return m, nil
		}

		m.list, _ = m.list.Update(msg)
		return m, nil

	// -----------------------------------------------------------------
	//  stateAddProcess
	// -----------------------------------------------------------------
	case stateAddProcess:
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "esc":
				m.state = stateList
				return m, nil

			case "ctrl+c", "q":
				m.killAllProcesses()
				return m, tea.Quit

			case "enter":
				cmdLine := strings.TrimSpace(m.textInput.Value())
				if cmdLine != "" {
					pItem, err := startProcess(m.program, len(m.processes), cmdLine)
					if err == nil {
						m.processes = append(m.processes, pItem)
						_ = m.addProcessToConfig(cmdLine)

						var newItems []list.Item
						for _, pr := range m.processes {
							newItems = append(newItems, processListItem{p: pr})
						}
						newItems = append(newItems, newProcessItem{})
						m.list.SetItems(newItems)

						m.currentProcessIndex = len(m.processes) - 1
						m.state = stateViewLogs
						m.justEnteredLogs = true

						if m.windowWidth > 0 && m.windowHeight > 0 {
							m.viewport.Width = m.windowWidth
							m.viewport.Height = m.windowHeight - 5
						}

						m = m.updateLogsViewport(true)
						return m, nil
					} else {
						log.Println("Error launching process:", err)
					}
				}
				m.state = stateList
				return m, nil
			}

		case tickMsg:
			return m, tickCmd()

		case tea.WindowSizeMsg:
			m.windowWidth = msg.Width
			m.windowHeight = msg.Height
			return m, nil
		}

		m.textInput, _ = m.textInput.Update(msg)
		return m, nil

	// -----------------------------------------------------------------
	//  stateViewLogs
	// -----------------------------------------------------------------
	case stateViewLogs:
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "esc":
				m.state = stateList
				return m, nil

			case "ctrl+c", "q":
				m.killAllProcesses()
				return m, tea.Quit

			case "ctrl+k":
				if m.currentProcessIndex >= 0 && m.currentProcessIndex < len(m.processes) {
					proc := m.processes[m.currentProcessIndex]
					if !proc.Finished && proc.Cmd != nil && proc.Cmd.Process != nil {
						err := proc.Cmd.Process.Kill()
						proc.mu.Lock()
						if err != nil {
							appendLog(proc, fmt.Sprintf("[Failed to kill process: %v]", err))
						} else {
							appendLog(proc, "[Process forcibly killed by user]")
							proc.Finished = true
						}
						proc.mu.Unlock()

						if m.program != nil {
							m.program.Send(logUpdateMsg{processIndex: m.currentProcessIndex})
						}
					}
				}
				return m, nil

			case "up", "k":
				m.viewport.LineUp(1)
			case "down", "j":
				m.viewport.LineDown(1)
			case "pgup":
				m.viewport.HalfViewUp()
			case "pgdown":
				m.viewport.HalfViewDown()
			case "home":
				m.viewport.GotoTop()
			case "end":
				m.viewport.GotoBottom()
			}

		case tea.MouseMsg:
			switch msg.Type {
			case tea.MouseWheelUp:
				m.viewport.LineUp(3)
			case tea.MouseWheelDown:
				m.viewport.LineDown(3)
			}

		case tickMsg:
			m = m.updateLogsViewport(false)
			return m, tickCmd()

		case logUpdateMsg:
			if m.currentProcessIndex == msg.processIndex {
				m = m.updateLogsViewport(m.viewport.AtBottom())
			}
			return m, nil

		case tea.WindowSizeMsg:
			m.windowWidth = msg.Width
			m.windowHeight = msg.Height

			m.viewport.Width = msg.Width
			m.viewport.Height = msg.Height - 5

			m = m.updateLogsViewport(false)
			return m, nil
		}

		m.viewport, _ = m.viewport.Update(msg)
		return m, nil
	}

	return m, nil
}

// -----------------------------------------------------------------------------
// View
// -----------------------------------------------------------------------------

func (m model) View() string {
	switch m.state {
	case stateList:
		return m.list.View()

	case stateAddProcess:
		return lipgloss.NewStyle().Padding(1, 2).Render(
			fmt.Sprintf(
				"Enter a command for a new process:\n\n%s\n\n(Enter – start, Esc – cancel, q/ctrl+c – quit)",
				m.textInput.View(),
			),
		)

	case stateViewLogs:
		if m.currentProcessIndex < 0 || m.currentProcessIndex >= len(m.processes) {
			return "Error: invalid process index"
		}
		proc := m.processes[m.currentProcessIndex]

		proc.mu.Lock()
		linesCount := len(proc.Logs)
		proc.mu.Unlock()

		return lipgloss.JoinVertical(
			lipgloss.Left,
			fmt.Sprintf("Viewing logs for process '%s' (total lines: %d)", proc.CmdLine, linesCount),
			"Controls: Esc – back, ↑/↓ – scroll, Home/End – top/bottom, ctrl+k – kill process, q/ctrl+c – quit",
			m.viewport.View(),
		)
	}

	return "Unknown state"
}

// -----------------------------------------------------------------------------
// Helper functions
// -----------------------------------------------------------------------------

func (m model) updateLogsViewport(forceGotoBottom bool) model {
	if m.currentProcessIndex < 0 || m.currentProcessIndex >= len(m.processes) {
		return m
	}
	proc := m.processes[m.currentProcessIndex]

	proc.mu.Lock()
	logs := strings.Join(proc.Logs, "\n")
	proc.mu.Unlock()

	currentTime := time.Now().Format("2006-01-02 15:04:05")
	header := fmt.Sprintf("Current time: %s\n", currentTime)
	linesInfo := fmt.Sprintf("Number of lines: %d\n---\n", len(proc.Logs))
	content := header + linesInfo + logs

	oldYOffset := m.viewport.YOffset
	wasAtBottom := m.viewport.AtBottom()

	m.viewport.SetContent(content)

	if forceGotoBottom || m.justEnteredLogs {
		m.viewport.GotoBottom()
		return model{
			state:               m.state,
			list:                m.list,
			textInput:           m.textInput,
			viewport:            m.viewport,
			processes:           m.processes,
			currentProcessIndex: m.currentProcessIndex,
			configPath:          m.configPath,
			justEnteredLogs:     false,
			program:             m.program,
			windowWidth:         m.windowWidth,
			windowHeight:        m.windowHeight,
		}
	}
	if wasAtBottom {
		m.viewport.GotoBottom()
	} else {
		m.viewport.YOffset = oldYOffset
	}
	return m
}

func (m *model) addProcessToConfig(cmdLine string) error {
	cfg, err := loadConfig(m.configPath)
	if err != nil {
		return err
	}
	cfg.Processes = append(cfg.Processes, cmdLine)
	return saveConfig(m.configPath, cfg)
}

func cp866ToUTF8(s string) (string, error) {
	// Placeholder for codepage conversion
	return s, nil
}

func (m *model) SetProgram(p *tea.Program) {
	m.program = p

	for i, proc := range m.processes {
		if proc.stdout != nil {
			go proc.readOutput(p, i, proc.stdout)
		}
		if proc.stderr != nil {
			go proc.readOutput(p, i, proc.stderr)
		}
	}
}

func main() {
	// Set UTF-8 in Windows console
	if runtime.GOOS == "windows" {
		_ = exec.Command("cmd", "/c", "chcp", "65001").Run()
	}

	m := initialModel(nil)

	p := tea.NewProgram(
		m,
		tea.WithAltScreen(),
	)

	m.SetProgram(p)

	if err := p.Start(); err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
}
