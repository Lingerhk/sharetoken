package modelinfo

import (
	"reflect"
	"testing"

	"sharetoken/internal/protocol"
)

// Every model of the table's source resolves to its own window and output
// limit, the ones left out of the table through the entry that covers them.
func TestLookupCoversItsSource(t *testing.T) {
	want := map[string][2]uint64{
		"claude-fable-5":                {1000000, 128000},
		"claude-fable-5-1":              {1000000, 128000},
		"claude-fable-5-2":              {1000000, 128000},
		"claude-haiku-4-5":              {200000, 64000},
		"claude-haiku-4-5-20251001":     {200000, 64000},
		"claude-haiku-5-5":              {1000000, 128000},
		"claude-opus-4-5":               {200000, 64000},
		"claude-opus-4-5-20251101":      {200000, 64000},
		"claude-opus-4-6":               {1000000, 128000},
		"claude-opus-4-7":               {1000000, 128000},
		"claude-opus-4-8":               {1000000, 128000},
		"claude-opus-5":                 {1000000, 128000},
		"claude-opus-5-5":               {1000000, 128000},
		"claude-sonnet-4-5":             {200000, 64000},
		"claude-sonnet-4-5-20250929":    {200000, 64000},
		"claude-sonnet-4-6":             {1000000, 128000},
		"claude-sonnet-5":               {1000000, 128000},
		"claude-sonnet-5-5":             {1000000, 128000},
		"gpt-4.1":                       {1047576, 32768},
		"gpt-4.1-mini":                  {1047576, 32768},
		"gpt-4.1-nano":                  {1047576, 32768},
		"gpt-4o":                        {128000, 16384},
		"gpt-4o-2024-05-13":             {128000, 4096},
		"gpt-4o-2024-08-06":             {128000, 16384},
		"gpt-4o-2024-11-20":             {128000, 16384},
		"gpt-4o-mini":                   {128000, 16384},
		"gpt-5":                         {272000, 128000},
		"gpt-5-mini":                    {272000, 128000},
		"gpt-5-nano":                    {272000, 128000},
		"gpt-5-pro":                     {272000, 272000},
		"gpt-5.1":                       {272000, 128000},
		"gpt-5.2":                       {272000, 128000},
		"gpt-5.2-pro":                   {272000, 128000},
		"gpt-5.3-codex":                 {272000, 128000},
		"gpt-5.3-codex-spark":           {100000, 32000},
		"gpt-5.4":                       {922000, 128000},
		"gpt-5.4-mini":                  {272000, 128000},
		"gpt-5.4-nano":                  {272000, 128000},
		"gpt-5.4-pro":                   {922000, 128000},
		"gpt-5.5":                       {922000, 128000},
		"gpt-5.5-pro":                   {922000, 128000},
		"gpt-5.6":                       {922000, 128000},
		"gpt-5.6-luna":                  {922000, 128000},
		"gpt-5.6-sol":                   {922000, 128000},
		"gpt-5.6-terra":                 {922000, 128000},
		"gpt-6-astra":                   {922000, 128000},
		"gpt-6-luna":                    {922000, 128000},
		"gpt-6-sol":                     {922000, 128000},
		"gpt-6.1-sol":                   {922000, 128000},
		"gpt-6.1-terra":                 {922000, 128000},
		"o1":                            {200000, 100000},
		"o1-pro":                        {200000, 100000},
		"o3":                            {200000, 100000},
		"o3-mini":                       {200000, 100000},
		"o3-pro":                        {200000, 100000},
		"o4-mini":                       {200000, 100000},
		"gemini-2.5-flash":              {1048576, 65536},
		"gemini-2.5-flash-lite":         {1048576, 65536},
		"gemini-2.5-pro":                {1048576, 65536},
		"gemini-3-flash-preview":        {1048576, 65536},
		"gemini-3.1-flash-lite":         {1048576, 65536},
		"gemini-3.1-flash-lite-preview": {1048576, 65536},
		"gemini-3.1-pro-preview":        {1048576, 65536},
		"gemini-3.5-flash":              {1048576, 65536},
		"gemini-3.5-flash-lite":         {1048576, 65536},
		"gemini-3.6-flash":              {1048576, 65536},
		"gemini-3.7-flash":              {1048576, 65536},
		"gemini-3.8-flash":              {1048576, 65536},
		"gemini-4-argon":                {1048576, 32768},
		"deepseek-flash":                {1048576, 393216},
		"deepseek-v4-flash":             {1048576, 393216},
		"deepseek-v4-pro":               {1048576, 393216},
		"grok-4.20-0309-non-reasoning":  {1000000, 30000},
		"grok-4.20-0309-reasoning":      {1000000, 30000},
		"grok-4.20-multi-agent-0309":    {1000000, 30000},
		"grok-4.3":                      {1000000, 30000},
		"grok-4.5":                      {500000, 500000},
		"grok-4.6":                      {500000, 500000},
		"grok-4.7":                      {500000, 500000},
	}
	for id, w := range want {
		m := Lookup(id)
		if m == nil || m.ContextWindow != w[0] || m.MaxOutputTokens != w[1] {
			t.Errorf("%s: got %+v, want window %d, output %d", id, m, w[0], w[1])
		}
	}
}

func TestLookup(t *testing.T) {
	cases := []struct {
		id   string
		want *protocol.ModelMeta
	}{
		{"claude-sonnet-4-5-20250929", &protocol.ModelMeta{ContextWindow: 200000, MaxOutputTokens: 64000, Reasoning: true}},
		{"claude-opus-4.6", &protocol.ModelMeta{ContextWindow: 1000000, MaxOutputTokens: 128000, Reasoning: true, ReasoningLevels: []string{"low", "medium", "high", "max"}}},
		{"claude-opus-4-6[1m]", &protocol.ModelMeta{ContextWindow: 1000000, MaxOutputTokens: 128000, Reasoning: true, ReasoningLevels: []string{"low", "medium", "high", "max"}}},
		{"anthropic/claude-opus-5-5", &protocol.ModelMeta{ContextWindow: 1000000, MaxOutputTokens: 128000, Reasoning: true, ReasoningLevels: []string{"low", "medium", "high", "xhigh", "max"}}},
		{"claude-3-5-sonnet-20241022", &protocol.ModelMeta{ContextWindow: 200000, MaxOutputTokens: 8192}},
		{"claude-opus-4-20250514", &protocol.ModelMeta{ContextWindow: 200000, MaxOutputTokens: 32000, Reasoning: true}},
		{"OpenAI/GPT-5.5", &protocol.ModelMeta{ContextWindow: 922000, MaxOutputTokens: 128000, Reasoning: true, ReasoningLevels: []string{"none", "low", "medium", "high", "xhigh"}}},
		{"gpt-5-codex", &protocol.ModelMeta{ContextWindow: 272000, MaxOutputTokens: 128000, Reasoning: true, ReasoningLevels: []string{"minimal", "low", "medium", "high"}}},
		{"gpt-4o-mini", &protocol.ModelMeta{ContextWindow: 128000, MaxOutputTokens: 16384}},
		{"models/gemini-2.5-pro", &protocol.ModelMeta{ContextWindow: 1048576, MaxOutputTokens: 65536, Reasoning: true}},
		{"deepseek-chat", &protocol.ModelMeta{ContextWindow: 128000, MaxOutputTokens: 8192}},
		{"deepseek-v4-pro", &protocol.ModelMeta{ContextWindow: 1048576, MaxOutputTokens: 393216, Reasoning: true, ReasoningLevels: []string{"low", "high", "max"}}},
		// a dot does not end a name: an unknown newer model is unknown
		{"gpt-5.9", nil},
		{"o30", nil},
		{"llama-3-70b", nil},
		{"", nil},
	}
	for _, c := range cases {
		if got := Lookup(c.id); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Lookup(%q) = %+v, want %+v", c.id, got, c.want)
		}
	}
}

func TestMerge(t *testing.T) {
	known := &protocol.ModelMeta{ContextWindow: 200000, MaxOutputTokens: 64000, Reasoning: true, ReasoningLevels: []string{"low", "high"}}
	cases := []struct {
		name       string
		known, own *protocol.ModelMeta
		want       *protocol.ModelMeta
	}{
		{"nothing", nil, nil, nil},
		{"table only", known, nil, known},
		{"own only", nil, &protocol.ModelMeta{ContextWindow: 32000}, &protocol.ModelMeta{ContextWindow: 32000}},
		{"own fields win", known, &protocol.ModelMeta{MaxOutputTokens: 8000, ReasoningLevels: []string{"medium"}},
			&protocol.ModelMeta{ContextWindow: 200000, MaxOutputTokens: 8000, Reasoning: true, ReasoningLevels: []string{"medium"}}},
		{"own levels make it reason", nil, &protocol.ModelMeta{ReasoningLevels: []string{"high", "low", "bogus"}},
			&protocol.ModelMeta{Reasoning: true, ReasoningLevels: []string{"low", "high"}}},
	}
	for _, c := range cases {
		if got := Merge(c.known, c.own); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}
