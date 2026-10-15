package rag

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

var ErrEmptyQuery = errors.New("rag query cannot be empty")

// Document 表示知识库中的一个文本分块。
// 它属于参考知识，不是当前事故的 Evidence。
type Document struct {
	ID               string   `json:"id"`
	Source           string   `json:"source"`
	Title            string   `json:"title"`
	Content          string   `json:"content"`
	RecommendedTools []string `json:"recommended_tools,omitempty"`
	Score            float64  `json:"score"`
}

// Retriever 是 Runtime 依赖的最小检索接口。
type Retriever interface {
	Search(ctx context.Context, query string, topK int) ([]Document, error)
}

type MarkdownRetriever struct {
	documents []Document
}

// NewMarkdownRetriever 加载 root 目录中的 Markdown 文档。
// 文档在初始化时读取，后续检索不重复访问磁盘。
func NewMarkdownRetriever(root string) (*MarkdownRetriever, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("rag root directory cannot be empty")
	}

	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat rag directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("rag path is not a directory: %s", root)
	}

	var documents []Document

	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}

		if !strings.EqualFold(filepath.Ext(path), ".md") &&
			!strings.EqualFold(filepath.Ext(path), ".markdown") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read knowledge document %s: %w", path, err)
		}

		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("resolve knowledge source: %w", err)
		}

		chunks := splitMarkdown(string(data))
		for i, chunk := range chunks {
			if strings.TrimSpace(chunk.Content) == "" {
				continue
			}

			documents = append(documents, Document{
				ID:               fmt.Sprintf("%s#%d", filepath.ToSlash(relative), i+1),
				Source:           filepath.ToSlash(relative),
				Title:            chunk.Title,
				Content:          chunk.Content,
				RecommendedTools: chunk.RecommendedTools,
			})
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load rag documents: %w", err)
	}

	if len(documents) == 0 {
		return nil, fmt.Errorf("no Markdown knowledge chunks found in %s", root)
	}

	return &MarkdownRetriever{documents: documents}, nil
}

func (r *MarkdownRetriever) Search(
	ctx context.Context,
	query string,
	topK int,
) ([]Document, error) {
	if r == nil {
		return nil, errors.New("rag retriever cannot be nil")
	}
	if ctx == nil {
		return nil, errors.New("rag context cannot be nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrEmptyQuery
	}
	if topK <= 0 {
		topK = 3
	}

	queryTerms := tokenize(query)
	if len(queryTerms) == 0 {
		return []Document{}, nil
	}

	type scoredDocument struct {
		document Document
		score    float64
	}

	scored := make([]scoredDocument, 0, len(r.documents))

	for _, doc := range r.documents {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		score := scoreDocument(queryTerms, doc)
		if score <= 0 {
			continue
		}

		doc.Score = score
		scored = append(scored, scoredDocument{
			document: doc,
			score:    score,
		})
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].document.ID < scored[j].document.ID
		}
		return scored[i].score > scored[j].score
	})

	if len(scored) > topK {
		scored = scored[:topK]
	}

	results := make([]Document, 0, len(scored))
	for _, item := range scored {
		results = append(results, item.document)
	}

	return results, nil
}

type markdownChunk struct {
	Title            string
	Content          string
	RecommendedTools []string
}

func splitMarkdown(content string) []markdownChunk {
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 4096), 1024*1024)

	title := ""
	var body []string
	var chunks []markdownChunk
	var recommendedTools []string
	inFrontMatter := false
	frontMatterSeen := false

	flush := func() {
		text := strings.TrimSpace(strings.Join(body, "\n"))
		if text == "" {
			return
		}

		chunks = append(chunks, markdownChunk{
			Title:            title,
			Content:          text,
			RecommendedTools: append([]string(nil), recommendedTools...),
		})
		body = nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if !frontMatterSeen && trimmed == "---" {
			frontMatterSeen = true
			inFrontMatter = true
			continue
		}

		if inFrontMatter {
			if trimmed == "---" {
				inFrontMatter = false
				continue
			}

			const key = "recommended_tools:"
			if strings.HasPrefix(strings.ToLower(trimmed), key) {
				value := strings.TrimSpace(trimmed[len(key):])
				value = strings.Trim(value, "[]")
				for _, name := range strings.Split(value, ",") {
					name = strings.Trim(strings.TrimSpace(name), `"'`)
					if name != "" {
						recommendedTools = append(recommendedTools, name)
					}
				}
			}
			continue
		}

		if strings.HasPrefix(trimmed, "#") {
			flush()
			title = strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			continue
		}

		body = append(body, line)
	}

	flush()
	return chunks
}

func scoreDocument(queryTerms []string, doc Document) float64 {
	titleTerms := tokenize(doc.Title)
	contentTerms := tokenize(doc.Content)

	titleSet := make(map[string]struct{}, len(titleTerms))
	contentSet := make(map[string]struct{}, len(contentTerms))

	for _, term := range titleTerms {
		titleSet[term] = struct{}{}
	}
	for _, term := range contentTerms {
		contentSet[term] = struct{}{}
	}

	var score float64
	for _, term := range queryTerms {
		if _, ok := titleSet[term]; ok {
			score += 3
		}
		if _, ok := contentSet[term]; ok {
			score += 1
		}
	}

	// 归一化，避免长文档仅凭词频获得不合理优势。
	if len(contentSet) > 0 {
		score /= 1 + 0.15*float64(len(contentSet))/100
	}

	return score
}

// tokenize 保留英文词，同时使用相邻汉字二元组匹配中文短语。
// 这是轻量级词法检索，不等同于语义向量检索。
func tokenize(text string) []string {
	text = strings.ToLower(text)

	var tokens []string
	var word strings.Builder
	var chineseRun []rune

	flushWord := func() {
		if word.Len() > 0 {
			tokens = append(tokens, word.String())
			word.Reset()
		}
	}

	flushChinese := func() {
		if len(chineseRun) == 1 {
			tokens = append(tokens, string(chineseRun[0]))
		}
		for i := 0; i+1 < len(chineseRun); i++ {
			tokens = append(tokens, string(chineseRun[i:i+2]))
		}
		chineseRun = nil
	}

	for _, ch := range text {
		if unicode.In(ch, unicode.Han) {
			flushWord()
			chineseRun = append(chineseRun, ch)
			continue
		}

		flushChinese()

		if unicode.IsLetter(ch) || unicode.IsDigit(ch) {
			word.WriteRune(ch)
		} else {
			flushWord()
		}
	}

	flushWord()
	flushChinese()

	return tokens
}
