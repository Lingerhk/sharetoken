package audit

import (
	"encoding/json"
	"fmt"
	mrand "math/rand/v2"
	"strings"
)

// Questions are open-ended: explanations, descriptions, advice, rewrites,
// continuations. A question with one right answer (arithmetic, spelling)
// is answered word for word by any capable model and tells nothing about
// which model answered; an open one shows the model's own voice in every
// token. They are built from topic x task x language, sometimes with a
// system prompt or an earlier turn, so they look like ordinary traffic.
// An operator's private pool (audit.prompts_file) replaces them.

// Challenge is one question as the auditor sends it.
type Challenge struct {
	System   string    `json:"system,omitempty"`
	Messages []Message `json:"messages"` // user/assistant turns, ending with the user's
}

// messages is the challenge as chat messages.
func (c Challenge) messages() []Message {
	var out []Message
	if c.System != "" {
		out = append(out, Message{Role: "system", Content: c.System})
	}
	return append(out, c.Messages...)
}

// ParsePrompts reads a prompt pool: one question per line, either plain text
// (one user message) or a JSON object {"system": "...", "messages": [...]}.
// Blank lines and lines starting with # are skipped.
func ParsePrompts(data string) ([]Challenge, error) {
	var out []Challenge
	for i, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, "{") {
			out = append(out, Challenge{Messages: []Message{{Role: "user", Content: line}}})
			continue
		}
		var c Challenge
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("prompts line %d: %w", i+1, err)
		}
		if n := len(c.Messages); n == 0 || c.Messages[n-1].Role != "user" {
			return nil, fmt.Errorf("prompts line %d: the messages must end with a user message", i+1)
		}
		for _, m := range c.Messages {
			if (m.Role != "user" && m.Role != "assistant") || m.Content == "" {
				return nil, fmt.Errorf("prompts line %d: messages are non-empty user or assistant turns", i+1)
			}
		}
		out = append(out, c)
	}
	return out, nil
}

var topicsEN = []string{
	"sourdough bread", "the history of the bicycle", "learning a second language as an adult", "home composting",
	"remote work", "public libraries", "how vaccines are tested", "saving for retirement", "night photography",
	"container gardening on a balcony", "running a first marathon", "unit tests in a small project", "jet lag",
	"the water cycle", "choosing a used car", "electric scooters in cities", "keeping houseplants alive",
	"writing a cover letter", "the printing press", "habits that improve sleep", "volcanoes", "minimalist travel",
	"code review etiquette", "the moon's phases", "how credit scores work", "making cold brew coffee",
	"birdwatching for beginners", "board games for families", "solar panels on a house", "the Silk Road",
	"database indexes", "teaching kids to ride a bike", "noise-cancelling headphones", "a weekly meal plan",
	"open-plan offices", "bees and pollination", "a job interview", "password managers", "the Roman aqueducts",
	"moving to a new city",
}

var topicsZH = []string{
	"手冲咖啡", "城市里的共享单车", "成年人学一门外语", "家庭阳台种菜", "远程办公", "公共图书馆", "疫苗是怎么做临床试验的",
	"年轻人存钱", "夜景摄影", "第一次跑马拉松", "给小项目写单元测试", "倒时差", "水循环", "买二手车", "养绿植",
	"写求职信", "活字印刷术", "改善睡眠的习惯", "火山", "轻装旅行", "代码评审", "月相变化", "信用分是怎么算的",
	"观鸟入门", "适合全家的桌游", "家用太阳能板", "丝绸之路", "数据库索引", "教孩子骑自行车", "降噪耳机",
	"一周的菜单", "开放式办公室", "蜜蜂和授粉", "工作面试", "密码管理器", "搬到一个新城市", "大熊猫", "茶文化",
	"地铁通勤", "整理房间",
}

var tasksEN = []string{
	"Explain %s to someone who has never thought about it.",
	"Give three practical tips about %s, with one sentence of reasoning each.",
	"What are the most common mistakes people make with %s?",
	"Write a short paragraph describing %s to a curious teenager.",
	"Compare two different approaches to %s and say when each one fits.",
	"Write the opening of a blog post about %s.",
	"I'm getting started with %s. What should I do in the first week?",
	"Summarize the pros and cons of %s in a few sentences.",
	"Tell a short anecdote that illustrates something about %s.",
	"What questions should I ask an expert on %s?",
	"Describe %s in a friendly, informal tone.",
	"Why do some people find %s frustrating, and what helps?",
}

var tasksZH = []string{
	"用通俗的话给从没接触过的人讲讲%s。",
	"关于%s，给三条实用建议，每条附一句理由。",
	"人们在%s上最常犯哪些错误？",
	"写一小段话，向好奇的中学生介绍%s。",
	"比较%s的两种不同做法，说说各适合什么情况。",
	"为一篇关于%s的博客写个开头。",
	"我刚开始接触%s，第一周应该做些什么？",
	"简要说说%s的优点和缺点。",
	"讲一个小故事，说明%s的某个特点。",
	"如果要请教%s方面的专家，我应该问哪些问题？",
	"用轻松随意的语气聊聊%s。",
	"为什么有人觉得%s很麻烦？有什么办法？",
}

var systemsEN = []string{
	"You are a helpful assistant.",
	"You are a concise assistant. Keep answers short.",
	"You are a friendly tutor who explains things step by step.",
}

var systemsZH = []string{
	"你是一个乐于助人的助手。",
	"你是一个简洁的助手，回答尽量简短。",
	"你是一位耐心的老师，喜欢一步步讲解。",
}

// An earlier exchange, for questions that arrive mid-conversation.
var openersEN = [][2]string{
	{"Hi! Can I ask you something?", "Of course! What would you like to know?"},
	{"I have a quick question for a project.", "Sure, go ahead and I'll do my best to help."},
}

var openersZH = [][2]string{
	{"你好，想问你个问题。", "当然可以，你想了解什么？"},
	{"我在准备一个小项目，有个问题。", "好的，请说，我尽量帮你。"},
}

// builtinChallenge draws a question from the built-in generator.
func builtinChallenge(r *mrand.Rand) Challenge {
	topics, tasks, systems, openers := topicsEN, tasksEN, systemsEN, openersEN
	if r.IntN(2) == 0 {
		topics, tasks, systems, openers = topicsZH, tasksZH, systemsZH, openersZH
	}
	c := Challenge{}
	q := fmt.Sprintf(tasks[r.IntN(len(tasks))], topics[r.IntN(len(topics))])
	if r.IntN(3) == 0 {
		c.System = systems[r.IntN(len(systems))]
	}
	if r.IntN(5) == 0 {
		o := openers[r.IntN(len(openers))]
		c.Messages = append(c.Messages, Message{Role: "user", Content: o[0]}, Message{Role: "assistant", Content: o[1]})
	}
	c.Messages = append(c.Messages, Message{Role: "user", Content: q})
	return c
}
