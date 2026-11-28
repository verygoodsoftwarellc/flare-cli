package command

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
)

// hostedTool is what `tools` shows of one entry in tools/list.
type hostedTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema struct {
		Properties map[string]schemaProperty `json:"properties"`
		Required   []string                  `json:"required"`
	} `json:"inputSchema"`
}

type schemaProperty struct {
	Type        json.RawMessage   `json:"type"`
	Description string            `json:"description"`
	Enum        []json.RawMessage `json:"enum"`
	Default     json.RawMessage   `json:"default"`
	Minimum     *json.Number      `json:"minimum"`
	Maximum     *json.Number      `json:"maximum"`
	MaxLength   *json.Number      `json:"maxLength"`
	MinItems    *json.Number      `json:"minItems"`
	MaxItems    *json.Number      `json:"maxItems"`
	Items       *schemaProperty   `json:"items"`
}

func newToolsCommand(options *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "tools [name]",
		Short: "List the tools on Flare's hosted MCP server, or show one",
		Long: "List the tools Flare's hosted MCP server offers your token, or show one tool's description and arguments. " +
			"Run any of them with `call`. --json prints the tools as the server lists them, input schemas included.",
		Example: "  flare-cli tools\n  flare-cli tools query_metrics",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			hosted, err := options.connectHostedMCP(command.Context())
			if err != nil {
				return err
			}
			defer hosted.endSession()
			tools, err := hosted.listTools(command.Context())
			if err != nil {
				return err
			}
			if len(args) == 0 {
				if options.json {
					return writeJSON(options.output, tools)
				}
				return writeToolTable(options.output, tools)
			}
			for _, raw := range tools {
				var tool hostedTool
				if json.Unmarshal(raw, &tool) != nil || tool.Name != args[0] {
					continue
				}
				if options.json {
					return writeJSON(options.output, raw)
				}
				return writeToolHelp(options.output, options.name, tool)
			}
			return fmt.Errorf("Flare has no tool named %q for this token; run `%s tools` to list them", args[0], options.name)
		},
	}
}

func newCallCommand(options *rootOptions) *cobra.Command {
	var jsonArgs string
	command := &cobra.Command{
		Use:   "call <tool> [key=value ...]",
		Short: "Run a tool on Flare's hosted MCP server",
		Long: "Run one of the tools `tools` lists, with its arguments as key=value pairs or a JSON object.\n\n" +
			"A value that parses as JSON is sent as that (42, true, [1,2], \"quoted\"); anything else is sent as a string. " +
			"Quote a value as JSON to send a string that would parse, as in name='\"true\"'. " +
			"--json-args takes the arguments as one JSON object, or - to read it from stdin; key=value pairs replace its keys.\n\n" +
			"Prints the tool's text, with JSON indented. --json prints its structured content, or its content list when it has none. " +
			"A tool that fails exits non-zero with its message on stderr.",
		Example: "  flare-cli call list_environments project_id=456\n" +
			"  flare-cli call get_environment_overviews environment_ids='[789]' hours=24\n" +
			"  echo '{\"environment_id\":789,\"group_by\":[\"target\"]}' | flare-cli call query_metrics --json-args -",
		Args: cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			arguments, err := toolArguments(jsonArgs, options.input, args[1:])
			if err != nil {
				return err
			}
			hosted, err := options.connectHostedMCP(command.Context())
			if err != nil {
				return err
			}
			defer hosted.endSession()
			result, err := hosted.callTool(command.Context(), args[0], arguments)
			if err != nil {
				return err
			}
			if result.IsError {
				message := strings.TrimSpace(withoutControls(toolText(result)))
				if message == "" {
					message = "the tool reported an error without a message"
				}
				return fmt.Errorf("%s: %s", args[0], message)
			}
			if options.json {
				if len(result.StructuredContent) > 0 && string(result.StructuredContent) != "null" {
					return writeJSON(options.output, result.StructuredContent)
				}
				return writeJSON(options.output, result.Content)
			}
			_, err = io.WriteString(options.output, toolText(result))
			return err
		},
	}
	command.Flags().StringVar(&jsonArgs, "json-args", "", "arguments as a JSON object, or - to read it from stdin")
	return command
}

// toolArguments builds a tools/call arguments object from --json-args and
// key=value pairs, keeping each value's JSON as written so large numbers
// pass through exactly.
func toolArguments(jsonArgs string, stdin io.Reader, pairs []string) (map[string]json.RawMessage, error) {
	arguments := map[string]json.RawMessage{}
	if jsonArgs != "" {
		data := []byte(jsonArgs)
		if jsonArgs == "-" {
			var err error
			if data, err = io.ReadAll(io.LimitReader(stdin, maxMCPMessage+1)); err != nil {
				return nil, fmt.Errorf("read --json-args from stdin: %w", err)
			}
			if len(data) > maxMCPMessage {
				return nil, fmt.Errorf("--json-args on stdin is over %d bytes", maxMCPMessage)
			}
		}
		if err := json.Unmarshal(data, &arguments); err != nil || arguments == nil {
			return nil, errors.New("--json-args must be a JSON object, like '{\"environment_id\":789}'")
		}
	}
	for _, pair := range pairs {
		key, value, found := strings.Cut(pair, "=")
		if !found || key == "" {
			return nil, fmt.Errorf("argument %q isn't key=value", pair)
		}
		if json.Valid([]byte(value)) {
			arguments[key] = json.RawMessage(value)
		} else {
			arguments[key], _ = json.Marshal(value)
		}
	}
	return arguments, nil
}

// toolText joins a result's text content, indenting any that is JSON, and
// notes content of other kinds, which only --json shows.
func toolText(result toolResult) string {
	var text strings.Builder
	for _, raw := range result.Content {
		var content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		_ = json.Unmarshal(raw, &content)
		if content.Type != "text" {
			fmt.Fprintf(&text, "(%s content; use --json to see it)\n", printable(content.Type))
			continue
		}
		piece := withoutControls(content.Text)
		var indented bytes.Buffer
		if json.Indent(&indented, []byte(content.Text), "", "  ") == nil {
			piece = indented.String()
		}
		text.WriteString(piece)
		if !strings.HasSuffix(piece, "\n") {
			text.WriteByte('\n')
		}
	}
	return text.String()
}

// withoutControls drops control characters other than newlines and tabs, so
// text from the server can't send escape sequences to the terminal.
func withoutControls(text string) string {
	return strings.Map(func(char rune) rune {
		if unicode.IsControl(char) && char != '\n' && char != '\t' {
			return -1
		}
		return char
	}, text)
}

func writeToolTable(output io.Writer, tools []json.RawMessage) error {
	rows := make([][]string, 0, len(tools))
	for _, raw := range tools {
		var tool hostedTool
		if json.Unmarshal(raw, &tool) != nil {
			continue
		}
		rows = append(rows, []string{tool.Name, summary(tool.Description)})
	}
	return writeTable(output, []string{"NAME", "DESCRIPTION"}, rows)
}

// summary is a description's lead: its first line, up to the end of the
// first sentence or a colon that starts a list of fields, outside any
// parentheses. Flare writes each description as one long line, often
// "Do this: field, field, ...".
func summary(description string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(description), "\n")
	depth := 0
	for index, char := range line {
		switch char {
		case '(':
			depth++
		case ')':
			depth = max(depth-1, 0)
		case '.', ':':
			if depth == 0 && strings.HasPrefix(line[index+1:], " ") {
				return line[:index] + "."
			}
		}
	}
	return line
}

func writeToolHelp(output io.Writer, name string, tool hostedTool) error {
	fmt.Fprintln(output, printable(tool.Name))
	fmt.Fprintln(output)
	for _, line := range strings.Split(strings.TrimSpace(tool.Description), "\n") {
		fmt.Fprintln(output, printable(line))
	}
	properties := tool.InputSchema.Properties
	if len(properties) == 0 {
		fmt.Fprintf(output, "\nTakes no arguments.\n\nRun it with:\n  %s call %s\n", name, printable(tool.Name))
		return nil
	}

	// Required arguments first, in the server's order, then the rest by name.
	required := map[string]bool{}
	var names []string
	for _, key := range tool.InputSchema.Required {
		if _, ok := properties[key]; ok && !required[key] {
			required[key] = true
			names = append(names, key)
		}
	}
	var optional []string
	for key := range properties {
		if !required[key] {
			optional = append(optional, key)
		}
	}
	slices.Sort(optional)
	names = append(names, optional...)

	rows := make([][]string, 0, len(names))
	example := []string{name, "call", printable(tool.Name)}
	for _, key := range names {
		property := properties[key]
		requiredCell := "no"
		if required[key] {
			requiredCell = "yes"
			example = append(example, printable(key)+"=<"+printable(property.typeName())+">")
		}
		rows = append(rows, []string{key, property.typeName(), requiredCell, property.details()})
	}
	fmt.Fprintln(output, "\nArguments:")
	if err := writeTable(output, []string{"NAME", "TYPE", "REQUIRED", "DETAILS"}, rows); err != nil {
		return err
	}
	_, err := fmt.Fprintf(output, "\nRun it with:\n  %s\n", strings.Join(example, " "))
	return err
}

// typeName is the property's JSON Schema type, such as integer, string|null
// or array of integer.
func (property schemaProperty) typeName() string {
	var name string
	var names []string
	switch {
	case json.Unmarshal(property.Type, &name) == nil:
	case json.Unmarshal(property.Type, &names) == nil:
		name = strings.Join(names, "|")
	default:
		name = "any"
	}
	if name == "array" && property.Items != nil {
		name += " of " + property.Items.typeName()
	}
	return name
}

// details says what values the property takes, in the schema's own terms.
func (property schemaProperty) details() string {
	var parts []string
	if property.Description != "" {
		parts = append(parts, property.Description)
	}
	parts = append(parts, property.constraints()...)
	if property.Items != nil {
		for _, constraint := range property.Items.constraints() {
			parts = append(parts, "each "+constraint)
		}
	}
	if property.MinItems != nil || property.MaxItems != nil {
		parts = append(parts, numberRange(property.MinItems, property.MaxItems)+" items")
	}
	if len(property.Default) > 0 {
		parts = append(parts, "default "+schemaValue(property.Default))
	}
	return strings.Join(parts, "; ")
}

func (property schemaProperty) constraints() []string {
	var parts []string
	if len(property.Enum) > 0 {
		values := make([]string, len(property.Enum))
		for index, value := range property.Enum {
			values[index] = schemaValue(value)
		}
		parts = append(parts, "one of "+strings.Join(values, ", "))
	}
	if property.Minimum != nil || property.Maximum != nil {
		parts = append(parts, numberRange(property.Minimum, property.Maximum))
	}
	if property.MaxLength != nil {
		parts = append(parts, "up to "+property.MaxLength.String()+" characters")
	}
	return parts
}

func numberRange(minimum, maximum *json.Number) string {
	switch {
	case minimum != nil && maximum != nil:
		return minimum.String() + " to " + maximum.String()
	case minimum != nil:
		return "at least " + minimum.String()
	}
	return "at most " + maximum.String()
}

// schemaValue shows a string without its quotes and anything else as JSON.
func schemaValue(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var compacted bytes.Buffer
	if json.Compact(&compacted, raw) != nil {
		return string(raw)
	}
	return compacted.String()
}
