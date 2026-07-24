package hub

import "testing"

// fakeTmux is the shared test double for the Tmux interface.
type fakeTmux struct {
	screen         string
	cmd            string
	panes          []Pane
	err            error
	sent           [][]string
	selected       []string
	splitID        string
	splits         []string    // split targets, in order
	respawns       [][2]string // {pane, cmd}
	killed         []string
	killedSessions []string
	paneOpts       map[string]string // "pane/name" -> value
	windowOpts     map[string]string // "session/name" -> value
	serverOpts     map[string]string // name -> value
	titles         [][2]string       // {pane, title} per SetPaneTitle call
	marked         string            // what FindMarkedPane returns
	created        [][3]string       // {name, dir, cmd}
}

func (f *fakeTmux) CapturePane(pane string) (string, error) { return f.screen, f.err }
func (f *fakeTmux) SendKeys(pane string, keys ...string) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, keys)
	return nil
}
func (f *fakeTmux) PaneCommand(pane string) (string, error) { return f.cmd, f.err }
func (f *fakeTmux) ListSessions() ([]Pane, error)           { return f.panes, f.err }
func (f *fakeTmux) SelectPane(pane string) error {
	if f.err != nil {
		return f.err
	}
	f.selected = append(f.selected, pane)
	return nil
}

func (f *fakeTmux) PaneSize(pane string) (int, int, error) {
	return 0, 0, f.err
}

func (f *fakeTmux) NewSession(name, dir, cmd string, width, height int) error {
	if f.err != nil {
		return f.err
	}
	f.created = append(f.created, [3]string{name, dir, cmd})
	return nil
}

func (f *fakeTmux) SplitWindow(target, cmd string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.splits = append(f.splits, target)
	return f.splitID, nil
}

func (f *fakeTmux) RespawnPane(pane, cmd string) error {
	if f.err != nil {
		return f.err
	}
	f.respawns = append(f.respawns, [2]string{pane, cmd})
	return nil
}

func (f *fakeTmux) KillPane(pane string) error {
	if f.err != nil {
		return f.err
	}
	f.killed = append(f.killed, pane)
	return nil
}

func (f *fakeTmux) KillSession(name string) error {
	if f.err != nil {
		return f.err
	}
	f.killedSessions = append(f.killedSessions, name)
	return nil
}

func (f *fakeTmux) SetPaneOption(pane, name, value string) error {
	if f.err != nil {
		return f.err
	}
	if f.paneOpts == nil {
		f.paneOpts = map[string]string{}
	}
	f.paneOpts[pane+"/"+name] = value
	return nil
}

func (f *fakeTmux) UnsetPaneOption(pane, name string) error {
	if f.err != nil {
		return f.err
	}
	delete(f.paneOpts, pane+"/"+name)
	return nil
}

func (f *fakeTmux) SetWindowOption(session, name, value string) error {
	if f.err != nil {
		return f.err
	}
	if f.windowOpts == nil {
		f.windowOpts = map[string]string{}
	}
	f.windowOpts[session+"/"+name] = value
	return nil
}

func (f *fakeTmux) SetServerOption(name, value string) error {
	if f.err != nil {
		return f.err
	}
	if f.serverOpts == nil {
		f.serverOpts = map[string]string{}
	}
	f.serverOpts[name] = value
	return nil
}

func (f *fakeTmux) SetPaneTitle(pane, title string) error {
	if f.err != nil {
		return f.err
	}
	f.titles = append(f.titles, [2]string{pane, title})
	return nil
}

func (f *fakeTmux) FindMarkedPane(session, option string) (string, error) {
	return f.marked, f.err
}

func (f *fakeTmux) ResizePane(pane string, width int) error { return f.err }

func (f *fakeTmux) ResizeWindow(session string, width, height int) error {
	return f.err
}

func (f *fakeTmux) HasPrimaryClient(session string) (bool, error) {
	return false, f.err
}

func (f *fakeTmux) ActivePane(session string) (string, error) {
	return "", f.err
}

func TestFakeImplementsTmux(t *testing.T) {
	var _ Tmux = (*fakeTmux)(nil)
	var _ Tmux = (*ExecTmux)(nil)
}
