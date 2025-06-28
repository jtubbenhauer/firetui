// Bubbletea Firestore TUI with two-pane layout
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"

	"cloud.google.com/go/firestore"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wrap"
)

type paneContext int

const (
	paneCollections paneContext = iota
	paneDocuments
	paneFields
)

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("69"))
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
)

type firestoreItem struct {
	title        string
	expanded     bool
	key          string
	rawValue     any
	valueStr     string
	isExpandable bool
}

func (i firestoreItem) Title() string       { return i.title }
func (i firestoreItem) Description() string { return "" }
func (i firestoreItem) FilterValue() string { return i.title }

func customDelegate() list.DefaultDelegate {
	d := list.NewDefaultDelegate()
	d.ShowDescription = false
	return d
}

type twoColumnDelegate struct {
	width int
}

func (d twoColumnDelegate) Height() int  { return 1 }
func (d twoColumnDelegate) Spacing() int { return 0 }
func (d twoColumnDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd {
	if msg, ok := msg.(tea.WindowSizeMsg); ok {
		d.width = msg.Width // full width, let JoinHorizontal handle split
	}
	return nil
}

func (d twoColumnDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	item, ok := listItem.(firestoreItem)
	if !ok {
		return
	}

	isSelected := index == m.Index()
	keyStyle := lipgloss.NewStyle().Width(30).Bold(true)
	valStyle := lipgloss.NewStyle()

	if isSelected {
		keyStyle = keyStyle.Foreground(lipgloss.Color("212"))
		valStyle = valStyle.Foreground(lipgloss.Color("212"))
	}

	key := keyStyle.Render(item.key)
	wrappedVal := wrap.String(item.valueStr, d.width-32)
	val := valStyle.Render(wrappedVal)

	fmt.Fprintf(w, "%s %s\n", key, val)
}

type model struct {
	client    *firestore.Client
	ctx       context.Context
	projectID string

	left     list.Model
	right    list.Model
	leftCtx  paneContext
	rightCtx paneContext
	path     []string
}

func initialModel(client *firestore.Client, ctx context.Context, projectId string) model {
	colItems := loadCollections(client, ctx)
	left := list.New(colItems, customDelegate(), 0, 0)
	left.Title = fmt.Sprintf("Collections (%s)", projectId)
	left.SetShowHelp(false)
	left.DisableQuitKeybindings()
	left.SetDelegate(customDelegate())

	fieldDelegate := twoColumnDelegate{}
	right := list.New([]list.Item{}, fieldDelegate, 0, 0)
	right.SetShowHelp(false)
	right.DisableQuitKeybindings()
	right.SetDelegate(fieldDelegate)

	return model{
		client:    client,
		ctx:       ctx,
		projectID: projectId,
		left:      left,
		right:     right,
		leftCtx:   paneCollections,
		rightCtx:  paneDocuments,
		path:      nil,
	}
}

func loadCollections(client *firestore.Client, ctx context.Context) []list.Item {
	cols, err := client.Collections(ctx).GetAll()
	if err != nil {
		log.Fatal(err)
	}
	var items []list.Item
	for _, col := range cols {
		items = append(items, firestoreItem{title: col.ID, key: col.ID})
	}
	return items
}

func loadDocuments(client *firestore.Client, ctx context.Context, coll string) []list.Item {
	docs, err := client.Collection(coll).Documents(ctx).GetAll()
	if err != nil {
		return []list.Item{firestoreItem{title: "<error>"}}
	}
	var items []list.Item
	for _, doc := range docs {
		items = append(items, firestoreItem{title: doc.Ref.ID, key: doc.Ref.ID})
	}
	return items
}

func loadFields(client *firestore.Client, ctx context.Context, coll, doc string) []list.Item {
	docSnap, err := client.Collection(coll).Doc(doc).Get(ctx)
	if err != nil {
		return []list.Item{firestoreItem{title: "<error>"}}
	}
	var items []list.Item
	for k, v := range docSnap.Data() {
		item := firestoreItem{
			key:          k,
			rawValue:     v,
			isExpandable: false,
		}
		switch v := v.(type) {
		case *firestore.DocumentRef:
			item.valueStr = v.Path
		case map[string]any, []any:
			item.valueStr = "<collapsed>"
			item.isExpandable = true
		default:
			item.valueStr = fmt.Sprintf("%v", v)
		}
		item.title = fmt.Sprintf("%s: %s", k, item.valueStr)
		items = append(items, item)
	}
	return items
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w := msg.Width
		h := msg.Height - 2
		m.left.SetSize(w/2, h)
		m.right.SetSize(w-w/2, h)
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit

		case "l", "enter":
			if len(m.path) == 0 {
				// Focused on left pane: selecting a collection
				item, ok := m.left.SelectedItem().(firestoreItem)
				if ok {
					m.path = []string{item.key}
					m.right.SetItems(loadDocuments(m.client, m.ctx, item.key))
					m.right.Select(0)
					m.leftCtx = paneCollections
					m.rightCtx = paneDocuments
				}
			} else if len(m.path) == 1 {
				// Focused on right pane: selecting a document
				item, ok := m.right.SelectedItem().(firestoreItem)
				if ok {
					m.path = append(m.path, item.key)
					m.left.SetItems(m.right.Items())
					m.left.Select(m.right.Index())
					m.leftCtx = paneDocuments
					m.right.SetItems(loadFields(m.client, m.ctx, m.path[0], m.path[1]))
					m.right.Select(0)
					m.rightCtx = paneFields
				}
			}

		// case "l", "enter":
		// 	if m.leftCtx == paneCollections && m.rightCtx == paneDocuments {
		// 		item, ok := m.right.SelectedItem().(firestoreItem)
		// 		if ok {
		// 			docID := item.key
		// 			m.path = append(m.path, docID)
		// 			m.left.SetItems(m.right.Items())
		// 			m.left.Select(m.right.Index())
		// 			m.leftCtx = paneDocuments
		// 			m.right.SetItems(loadFields(m.client, m.ctx, m.path[0], m.path[1]))
		// 			m.right.Select(0)
		// 			m.rightCtx = paneFields
		// 		}
		// 	} else if m.leftCtx == paneCollections && m.rightCtx == paneDocuments && len(m.path) == 0 {
		// 		item, ok := m.left.SelectedItem().(firestoreItem)
		// 		if ok {
		// 			m.path = []string{item.key}
		// 			m.right.SetItems(loadDocuments(m.client, m.ctx, item.key))
		// 			m.right.Select(0)
		// 			m.leftCtx = paneCollections
		// 			m.rightCtx = paneDocuments
		// 		}
		// 	}

		case "h":
			if len(m.path) > 1 {
				m.path = m.path[:len(m.path)-1]
				m.right.SetItems(loadDocuments(m.client, m.ctx, m.path[0]))
				m.right.Select(0)
				m.leftCtx = paneCollections
				m.rightCtx = paneDocuments
			} else if len(m.path) == 1 {
				m.path = m.path[:0]
				m.right.SetItems(nil)
				m.left.SetItems(loadCollections(m.client, m.ctx))
				m.leftCtx = paneCollections
				m.rightCtx = paneDocuments
			}

		case "j":
			m.right.CursorDown()
		case "k":
			m.right.CursorUp()
		case "d":
			m.right.CursorDown()
			m.right.CursorDown()
			m.right.CursorDown()
		case "u":
			m.right.CursorUp()
			m.right.CursorUp()
			m.right.CursorUp()
		}
	}

	var cmd tea.Cmd
	m.left, _ = m.left.Update(msg)
	m.right, cmd = m.right.Update(msg)
	return m, cmd
}

func (m model) View() string {
	return lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(50).Render(m.left.View()),
		lipgloss.NewStyle().Width(0).MaxWidth(0).Render(m.right.View()),
	) + "\n[j/k to move, l to enter, h to back, q to quit]"
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: firestore-tui <projectId>")
		os.Exit(1)
	}
	ctx := context.Background()
	projectId := os.Args[1]
	client, err := firestore.NewClient(ctx, projectId)
	if err != nil {
		log.Fatalf("failed to create client: %v", err)
	}
	defer client.Close()

	p := tea.NewProgram(initialModel(client, ctx, projectId))
	if _, err := p.Run(); err != nil {
		fmt.Println("Error running program:", err)
		os.Exit(1)
	}
}
