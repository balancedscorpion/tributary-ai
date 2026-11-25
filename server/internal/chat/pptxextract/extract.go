package pptxextract

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	Version                    = 1
	DefaultMaxEntries          = 4000
	DefaultMaxUncompressedSize = 768 << 20
	DefaultExternalTimeout     = 90 * time.Second
)

type StageStatus string

const (
	StageCompleted StageStatus = "completed"
	StageSkipped   StageStatus = "skipped"
	StageFailed    StageStatus = "failed"
)

type ManifestStatus string

const (
	StatusComplete ManifestStatus = "complete"
	StatusPartial  ManifestStatus = "partial"
	StatusFailed   ManifestStatus = "failed"
)

type Request struct {
	SourcePath             string
	FileName               string
	OutputDir              string
	SourceSHA256           string
	IncludeRawXML          bool
	EnableMarkItDown       bool
	EnableRendering        bool
	EnableOCR              bool
	ExternalCommandTimeout time.Duration
	MaxEntries             int
	MaxUncompressedBytes   int64
}

type Result struct {
	Manifest  Manifest
	Deck      Deck
	Graph     DeckGraph
	OutputDir string
}

type Manifest struct {
	Version      int             `json:"version"`
	Pipeline     string          `json:"pipeline"`
	Status       ManifestStatus  `json:"status"`
	FileName     string          `json:"fileName"`
	SourceSHA256 string          `json:"sourceSha256,omitempty"`
	SlideCount   int             `json:"slideCount"`
	CreatedAt    time.Time       `json:"createdAt"`
	Outputs      map[string]any  `json:"outputs"`
	Capabilities map[string]bool `json:"capabilities"`
	Stages       []Stage         `json:"stages"`
}

type Stage struct {
	Name    string      `json:"name"`
	Status  StageStatus `json:"status"`
	Detail  string      `json:"detail,omitempty"`
	Outputs []string    `json:"outputs,omitempty"`
	Error   string      `json:"error,omitempty"`
}

type DeckGraph struct {
	Version      int           `json:"version"`
	FileName     string        `json:"fileName"`
	SlideCount   int           `json:"slideCount"`
	ContentTypes []ContentType `json:"contentTypes,omitempty"`
	Slides       []SlideGraph  `json:"slides"`
}

type ContentType struct {
	Part        string `json:"part"`
	ContentType string `json:"contentType"`
}

type SlideGraph struct {
	Number         int               `json:"number"`
	RelationshipID string            `json:"relationshipId,omitempty"`
	SlideID        string            `json:"slideId,omitempty"`
	XMLPath        string            `json:"xmlPath"`
	Hidden         bool              `json:"hidden,omitempty"`
	LayoutPath     string            `json:"layoutPath,omitempty"`
	NotesPath      string            `json:"notesPath,omitempty"`
	CommentPaths   []string          `json:"commentPaths,omitempty"`
	ChartPaths     []string          `json:"chartPaths,omitempty"`
	MediaPaths     []string          `json:"mediaPaths,omitempty"`
	Hyperlinks     []Hyperlink       `json:"hyperlinks,omitempty"`
	Relationships  []RelationshipRef `json:"relationships,omitempty"`
}

type Deck struct {
	Version    int     `json:"version"`
	FileName   string  `json:"fileName"`
	SlideCount int     `json:"slideCount"`
	Slides     []Slide `json:"slides"`
	Text       string  `json:"text,omitempty"`
}

type Slide struct {
	Number        int         `json:"number"`
	XMLPath       string      `json:"xmlPath"`
	Hidden        bool        `json:"hidden,omitempty"`
	Title         string      `json:"title,omitempty"`
	Text          []TextBlock `json:"text,omitempty"`
	Notes         []TextBlock `json:"notes,omitempty"`
	Comments      []Comment   `json:"comments,omitempty"`
	Charts        []Chart     `json:"charts,omitempty"`
	Media         []MediaRef  `json:"media,omitempty"`
	Hyperlinks    []Hyperlink `json:"hyperlinks,omitempty"`
	OCRText       string      `json:"ocrText,omitempty"`
	RenderPath    string      `json:"renderPath,omitempty"`
	ThumbnailPath string      `json:"thumbnailPath,omitempty"`
	RawText       string      `json:"rawText,omitempty"`
}

type TextBlock struct {
	Text             string  `json:"text"`
	Source           string  `json:"source"`
	XMLPath          string  `json:"xmlPath,omitempty"`
	ShapeID          string  `json:"shapeId,omitempty"`
	ShapeName        string  `json:"shapeName,omitempty"`
	ShapeDescription string  `json:"shapeDescription,omitempty"`
	PlaceholderType  string  `json:"placeholderType,omitempty"`
	BoundingBox      *Box    `json:"boundingBox,omitempty"`
	Confidence       float64 `json:"confidence"`
}

type Box struct {
	X  int64 `json:"x"`
	Y  int64 `json:"y"`
	CX int64 `json:"cx"`
	CY int64 `json:"cy"`
}

type Comment struct {
	Text       string  `json:"text"`
	AuthorID   string  `json:"authorId,omitempty"`
	CreatedAt  string  `json:"createdAt,omitempty"`
	XMLPath    string  `json:"xmlPath,omitempty"`
	Confidence float64 `json:"confidence"`
}

type Chart struct {
	RelationshipID       string        `json:"relationshipId,omitempty"`
	XMLPath              string        `json:"xmlPath"`
	Title                string        `json:"title,omitempty"`
	Text                 []string      `json:"text,omitempty"`
	Series               []ChartSeries `json:"series,omitempty"`
	EmbeddedWorkbookPath string        `json:"embeddedWorkbookPath,omitempty"`
}

type ChartSeries struct {
	Name       string   `json:"name,omitempty"`
	Categories []string `json:"categories,omitempty"`
	Values     []string `json:"values,omitempty"`
}

type MediaRef struct {
	RelationshipID string `json:"relationshipId,omitempty"`
	XMLPath        string `json:"xmlPath"`
	ContentType    string `json:"contentType,omitempty"`
}

type Hyperlink struct {
	RelationshipID string `json:"relationshipId,omitempty"`
	Target         string `json:"target"`
	External       bool   `json:"external,omitempty"`
}

type RelationshipRef struct {
	ID         string `json:"id"`
	Type       string `json:"type,omitempty"`
	Target     string `json:"target,omitempty"`
	TargetMode string `json:"targetMode,omitempty"`
	Resolved   string `json:"resolved,omitempty"`
}

type packageReader struct {
	path         string
	reader       *zip.ReadCloser
	files        map[string]*zip.File
	contentTypes contentTypeIndex
}

type contentTypeIndex struct {
	defaults  map[string]string
	overrides map[string]string
}

type relationshipFile struct {
	Relationships []relationshipXML `xml:"Relationship"`
}

type relationshipXML struct {
	ID         string `xml:"Id,attr"`
	Type       string `xml:"Type,attr"`
	Target     string `xml:"Target,attr"`
	TargetMode string `xml:"TargetMode,attr"`
}

type slideListItem struct {
	SlideID        string
	RelationshipID string
	Hidden         bool
}

func Extract(ctx context.Context, req Request) (Result, error) {
	sourcePath := strings.TrimSpace(req.SourcePath)
	if sourcePath == "" {
		return Result{}, errors.New("pptxextract: source path is required")
	}
	outputDir := strings.TrimSpace(req.OutputDir)
	if outputDir == "" {
		return Result{}, errors.New("pptxextract: output dir is required")
	}
	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		fileName = filepath.Base(sourcePath)
	}
	timeout := req.ExternalCommandTimeout
	if timeout <= 0 {
		timeout = DefaultExternalTimeout
	}
	if err := os.RemoveAll(outputDir); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return Result{}, err
	}

	builder := &manifestBuilder{
		manifest: Manifest{
			Version:      Version,
			Pipeline:     "pptx",
			Status:       StatusComplete,
			FileName:     fileName,
			SourceSHA256: strings.TrimSpace(req.SourceSHA256),
			CreatedAt:    time.Now().UTC(),
			Outputs:      map[string]any{},
			Capabilities: map[string]bool{},
		},
	}

	pkg, err := openPackage(sourcePath, req.MaxEntries, req.MaxUncompressedBytes)
	if err != nil {
		builder.stage(Stage{Name: "intake_validation", Status: StageFailed, Error: err.Error()})
		_ = writeJSON(filepath.Join(outputDir, "manifest.json"), builder.finish())
		return Result{Manifest: builder.manifest, OutputDir: outputDir}, err
	}
	defer pkg.close()
	builder.stage(Stage{Name: "intake_validation", Status: StageCompleted, Detail: "Valid PPTX OOXML package"})

	deck, graph, err := pkg.extractDeck(fileName)
	if err != nil {
		builder.stage(Stage{Name: "ooxml_structure", Status: StageFailed, Error: err.Error()})
		_ = writeJSON(filepath.Join(outputDir, "manifest.json"), builder.finish())
		return Result{Manifest: builder.manifest, OutputDir: outputDir}, err
	}
	builder.stage(Stage{
		Name:   "ooxml_structure",
		Status: StageCompleted,
		Detail: fmt.Sprintf("Extracted %d slides from OOXML relationships and slide XML", len(deck.Slides)),
	})

	if req.EnableMarkItDown {
		if rel, st := runMarkItDown(ctx, sourcePath, outputDir, timeout); st.Status == StageCompleted {
			builder.stage(st)
			builder.output("markitdownMarkdownPath", rel)
		} else {
			builder.stage(st)
		}
	} else {
		builder.stage(Stage{Name: "markitdown_text", Status: StageSkipped, Detail: "External MarkItDown stage disabled"})
	}

	renderedSlides := map[int]string{}
	if req.EnableRendering {
		renderRelPaths, st := renderSlides(ctx, sourcePath, outputDir, timeout)
		if st.Status == StageFailed && !req.EnableMarkItDown && !req.EnableOCR {
			builder.optionalStage(st)
		} else {
			builder.stage(st)
		}
		if st.Status == StageCompleted {
			for i, rel := range renderRelPaths {
				renderedSlides[i+1] = rel
			}
			builder.output("renderedSlides", renderRelPaths)
		}
	} else {
		builder.stage(Stage{Name: "visual_render", Status: StageSkipped, Detail: "External rendering stage disabled"})
	}
	for i := range deck.Slides {
		if rel := renderedSlides[deck.Slides[i].Number]; rel != "" {
			deck.Slides[i].RenderPath = rel
		}
	}

	if req.EnableOCR {
		if len(renderedSlides) == 0 {
			builder.stage(Stage{Name: "ocr", Status: StageSkipped, Detail: "No rendered slides available for OCR"})
		} else {
			ocrOutputs, st := runOCR(ctx, outputDir, renderedSlides, &deck, timeout)
			builder.stage(st)
			if st.Status == StageCompleted {
				builder.output("ocrText", ocrOutputs)
			}
		}
	} else {
		builder.stage(Stage{Name: "ocr", Status: StageSkipped, Detail: "External OCR stage disabled"})
	}

	deck.Text = deckPlainText(deck)
	nativeMarkdown := renderMarkdown(deck)
	if err := writeFile(filepath.Join(outputDir, "deck.md"), []byte(nativeMarkdown)); err != nil {
		return Result{}, err
	}
	if err := writeJSON(filepath.Join(outputDir, "deck.json"), deck); err != nil {
		return Result{}, err
	}
	if err := writeJSON(filepath.Join(outputDir, "deck_graph.json"), graph); err != nil {
		return Result{}, err
	}
	slideRelPaths := make([]string, 0, len(deck.Slides))
	for _, slide := range deck.Slides {
		rel := path.Join("slides", fmt.Sprintf("slide-%03d.json", slide.Number))
		if err := writeJSON(filepath.Join(outputDir, filepath.FromSlash(rel)), slide); err != nil {
			return Result{}, err
		}
		slideRelPaths = append(slideRelPaths, rel)
	}
	builder.stage(Stage{
		Name:    "canonical_outputs",
		Status:  StageCompleted,
		Detail:  "Wrote provider-agnostic deck JSON, slide JSON, and Markdown",
		Outputs: append([]string{"deck.json", "deck_graph.json", "deck.md"}, slideRelPaths...),
	})
	builder.output("deckPath", "deck.json")
	builder.output("graphPath", "deck_graph.json")
	builder.output("markdownPath", "deck.md")
	builder.output("slidePaths", slideRelPaths)

	if req.IncludeRawXML {
		rawOutputs, err := pkg.writeRawXML(filepath.Join(outputDir, "raw", "ooxml"))
		if err != nil {
			builder.stage(Stage{Name: "raw_ooxml", Status: StageFailed, Error: err.Error()})
		} else {
			builder.stage(Stage{Name: "raw_ooxml", Status: StageCompleted, Detail: "Copied raw XML and relationship parts", Outputs: rawOutputs})
			builder.output("rawOOXMLRoot", "raw/ooxml")
		}
	} else {
		builder.stage(Stage{Name: "raw_ooxml", Status: StageSkipped, Detail: "Raw OOXML copy disabled"})
	}

	builder.manifest.SlideCount = len(deck.Slides)
	builder.manifest.Capabilities = capabilities(deck)
	manifest := builder.finish()
	if err := writeJSON(filepath.Join(outputDir, "manifest.json"), manifest); err != nil {
		return Result{}, err
	}
	builder.output("manifestPath", "manifest.json")
	manifest.Outputs = builder.manifest.Outputs
	if err := writeJSON(filepath.Join(outputDir, "manifest.json"), manifest); err != nil {
		return Result{}, err
	}
	return Result{
		Manifest:  manifest,
		Deck:      deck,
		Graph:     graph,
		OutputDir: outputDir,
	}, nil
}

func (r Result) Metadata(rootRelPath string) map[string]any {
	rootRelPath = strings.Trim(strings.TrimSpace(filepath.ToSlash(rootRelPath)), "/")
	rel := func(p string) string {
		p = strings.Trim(strings.TrimSpace(filepath.ToSlash(p)), "/")
		if rootRelPath == "" || p == "" {
			return p
		}
		return path.Join(rootRelPath, p)
	}
	meta := map[string]any{
		"kind":         "presentation",
		"pipeline":     "pptx",
		"version":      r.Manifest.Version,
		"status":       string(r.Manifest.Status),
		"slideCount":   r.Manifest.SlideCount,
		"rootPath":     rootRelPath,
		"manifestPath": rel("manifest.json"),
		"deckPath":     rel("deck.json"),
		"graphPath":    rel("deck_graph.json"),
		"markdownPath": rel("deck.md"),
		"capabilities": r.Manifest.Capabilities,
		"outputs":      r.Manifest.Outputs,
		"stages":       r.Manifest.Stages,
	}
	if rendered, ok := r.Manifest.Outputs["renderedSlides"].([]string); ok && len(rendered) > 0 {
		slides := make([]string, 0, len(rendered))
		for _, slide := range rendered {
			if clean := rel(slide); clean != "" {
				slides = append(slides, clean)
			}
		}
		if len(slides) > 0 {
			meta["renderedSlides"] = slides
			meta["previewImagePath"] = slides[0]
		}
	}
	if r.Manifest.SourceSHA256 != "" {
		meta["sourceSha256"] = r.Manifest.SourceSHA256
	}
	return meta
}

func FailedMetadata(err error) map[string]any {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return map[string]any{
		"kind":     "presentation",
		"pipeline": "pptx",
		"version":  Version,
		"status":   string(StatusFailed),
		"error":    msg,
	}
}

func openPackage(sourcePath string, maxEntries int, maxUncompressedBytes int64) (*packageReader, error) {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	if maxUncompressedBytes <= 0 {
		maxUncompressedBytes = DefaultMaxUncompressedSize
	}
	zr, err := zip.OpenReader(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("open pptx zip: %w", err)
	}
	pkg := &packageReader{
		path:   sourcePath,
		reader: zr,
		files:  make(map[string]*zip.File, len(zr.File)),
	}
	var total uint64
	for _, f := range zr.File {
		clean, ok := cleanZipName(f.Name)
		if !ok {
			_ = zr.Close()
			return nil, fmt.Errorf("unsafe pptx entry path %q", f.Name)
		}
		if f.FileInfo().IsDir() {
			continue
		}
		total += f.UncompressedSize64
		if len(pkg.files)+1 > maxEntries {
			_ = zr.Close()
			return nil, fmt.Errorf("pptx has too many entries: %d > %d", len(pkg.files)+1, maxEntries)
		}
		if total > uint64(maxUncompressedBytes) {
			_ = zr.Close()
			return nil, fmt.Errorf("pptx uncompressed size exceeds limit: %d > %d", total, maxUncompressedBytes)
		}
		if _, exists := pkg.files[clean]; !exists {
			pkg.files[clean] = f
		}
	}
	if !pkg.has("[Content_Types].xml") {
		_ = zr.Close()
		return nil, errors.New("pptx missing [Content_Types].xml")
	}
	if !pkg.has("ppt/presentation.xml") {
		_ = zr.Close()
		return nil, errors.New("pptx missing ppt/presentation.xml")
	}
	contentTypes, err := pkg.parseContentTypes()
	if err != nil {
		_ = zr.Close()
		return nil, err
	}
	pkg.contentTypes = contentTypes
	return pkg, nil
}

func (p *packageReader) close() {
	if p != nil && p.reader != nil {
		_ = p.reader.Close()
	}
}

func (p *packageReader) has(name string) bool {
	_, ok := p.files[strings.Trim(strings.TrimSpace(filepath.ToSlash(name)), "/")]
	return ok
}

func (p *packageReader) read(name string) ([]byte, error) {
	clean, ok := cleanZipName(name)
	if !ok {
		return nil, fmt.Errorf("unsafe pptx entry path %q", name)
	}
	f, ok := p.files[clean]
	if !ok {
		return nil, os.ErrNotExist
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func cleanZipName(name string) (string, bool) {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if name == "" || strings.ContainsRune(name, '\x00') || strings.HasPrefix(name, "/") {
		return "", false
	}
	u, err := url.PathUnescape(name)
	if err == nil {
		name = u
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, ":") {
		return "", false
	}
	return clean, true
}

func (p *packageReader) parseContentTypes() (contentTypeIndex, error) {
	raw, err := p.read("[Content_Types].xml")
	if err != nil {
		return contentTypeIndex{}, err
	}
	type defaultXML struct {
		Extension   string `xml:"Extension,attr"`
		ContentType string `xml:"ContentType,attr"`
	}
	type overrideXML struct {
		PartName    string `xml:"PartName,attr"`
		ContentType string `xml:"ContentType,attr"`
	}
	type typesXML struct {
		Defaults  []defaultXML  `xml:"Default"`
		Overrides []overrideXML `xml:"Override"`
	}
	var parsed typesXML
	if err := xml.Unmarshal(raw, &parsed); err != nil {
		return contentTypeIndex{}, fmt.Errorf("parse [Content_Types].xml: %w", err)
	}
	idx := contentTypeIndex{defaults: map[string]string{}, overrides: map[string]string{}}
	for _, d := range parsed.Defaults {
		ext := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d.Extension), "."))
		ct := strings.TrimSpace(d.ContentType)
		if ext != "" && ct != "" {
			idx.defaults[ext] = ct
		}
	}
	for _, o := range parsed.Overrides {
		part := strings.Trim(strings.TrimSpace(o.PartName), "/")
		ct := strings.TrimSpace(o.ContentType)
		if part != "" && ct != "" {
			idx.overrides[part] = ct
		}
	}
	return idx, nil
}

func (c contentTypeIndex) contentType(part string) string {
	part = strings.Trim(strings.TrimSpace(filepath.ToSlash(part)), "/")
	if ct := c.overrides[part]; ct != "" {
		return ct
	}
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(part), "."))
	return c.defaults[ext]
}

func (c contentTypeIndex) list() []ContentType {
	out := make([]ContentType, 0, len(c.overrides))
	for part, ct := range c.overrides {
		out = append(out, ContentType{Part: part, ContentType: ct})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Part < out[j].Part })
	return out
}

func (p *packageReader) extractDeck(fileName string) (Deck, DeckGraph, error) {
	presentationXML, err := p.read("ppt/presentation.xml")
	if err != nil {
		return Deck{}, DeckGraph{}, err
	}
	presentationRels, err := p.relationshipsFor("ppt/presentation.xml")
	if err != nil {
		return Deck{}, DeckGraph{}, err
	}
	slideItems, err := parseSlideList(presentationXML)
	if err != nil {
		return Deck{}, DeckGraph{}, err
	}
	if len(slideItems) == 0 {
		return Deck{}, DeckGraph{}, errors.New("pptx contains no slides in presentation.xml")
	}
	deck := Deck{Version: Version, FileName: fileName}
	graph := DeckGraph{Version: Version, FileName: fileName, ContentTypes: p.contentTypes.list()}
	for idx, item := range slideItems {
		rel, ok := presentationRels[item.RelationshipID]
		if !ok {
			return Deck{}, DeckGraph{}, fmt.Errorf("slide relationship %s not found", item.RelationshipID)
		}
		slidePath := resolveRelationshipTarget("ppt/presentation.xml", rel.Target)
		slide, slideGraph, err := p.extractSlide(idx+1, item, slidePath)
		if err != nil {
			return Deck{}, DeckGraph{}, err
		}
		deck.Slides = append(deck.Slides, slide)
		graph.Slides = append(graph.Slides, slideGraph)
	}
	deck.SlideCount = len(deck.Slides)
	graph.SlideCount = len(graph.Slides)
	return deck, graph, nil
}

func parseSlideList(raw []byte) ([]slideListItem, error) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var slides []slideListItem
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "sldId" {
			continue
		}
		item := slideListItem{}
		for _, attr := range start.Attr {
			switch {
			case attr.Name.Local == "id" && attr.Name.Space == "":
				item.SlideID = attr.Value
			case attr.Name.Local == "id":
				item.RelationshipID = attr.Value
			case attr.Name.Local == "show" && attr.Value == "0":
				item.Hidden = true
			}
		}
		if item.RelationshipID != "" {
			slides = append(slides, item)
		}
	}
	return slides, nil
}

func (p *packageReader) relationshipsFor(part string) (map[string]relationshipXML, error) {
	relsPath := relationshipsPath(part)
	raw, err := p.read(relsPath)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]relationshipXML{}, nil
	}
	if err != nil {
		return nil, err
	}
	var parsed relationshipFile
	if err := xml.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("parse %s: %w", relsPath, err)
	}
	out := make(map[string]relationshipXML, len(parsed.Relationships))
	for _, rel := range parsed.Relationships {
		if strings.TrimSpace(rel.ID) != "" {
			out[rel.ID] = rel
		}
	}
	return out, nil
}

func relationshipsPath(part string) string {
	part = strings.Trim(strings.TrimSpace(filepath.ToSlash(part)), "/")
	dir, base := path.Split(part)
	return path.Join(dir, "_rels", base+".rels")
}

func resolveRelationshipTarget(sourcePart string, target string) string {
	target = strings.TrimSpace(strings.ReplaceAll(target, "\\", "/"))
	if target == "" {
		return ""
	}
	if strings.HasPrefix(target, "/") {
		return strings.Trim(path.Clean(target), "/")
	}
	base := path.Dir(strings.Trim(strings.TrimSpace(filepath.ToSlash(sourcePart)), "/"))
	return strings.Trim(path.Clean(path.Join(base, target)), "/")
}

func (p *packageReader) extractSlide(number int, item slideListItem, slidePath string) (Slide, SlideGraph, error) {
	raw, err := p.read(slidePath)
	if err != nil {
		return Slide{}, SlideGraph{}, fmt.Errorf("read slide %s: %w", slidePath, err)
	}
	textBlocks, err := parseSlideText(slidePath, raw)
	if err != nil {
		return Slide{}, SlideGraph{}, fmt.Errorf("parse slide %s text: %w", slidePath, err)
	}
	rels, err := p.relationshipsFor(slidePath)
	if err != nil {
		return Slide{}, SlideGraph{}, err
	}

	slide := Slide{
		Number:  number,
		XMLPath: slidePath,
		Hidden:  item.Hidden,
		Text:    textBlocks,
		RawText: strings.Join(textBlockTexts(textBlocks), "\n"),
	}
	slide.Title = chooseSlideTitle(textBlocks)
	graph := SlideGraph{
		Number:         number,
		RelationshipID: item.RelationshipID,
		SlideID:        item.SlideID,
		XMLPath:        slidePath,
		Hidden:         item.Hidden,
	}

	relationshipIDs := make([]string, 0, len(rels))
	for id := range rels {
		relationshipIDs = append(relationshipIDs, id)
	}
	sort.Strings(relationshipIDs)
	for _, id := range relationshipIDs {
		rel := rels[id]
		resolved := resolveRelationshipTarget(slidePath, rel.Target)
		graph.Relationships = append(graph.Relationships, RelationshipRef{
			ID:         rel.ID,
			Type:       rel.Type,
			Target:     rel.Target,
			TargetMode: rel.TargetMode,
			Resolved:   resolved,
		})
		typ := strings.ToLower(rel.Type)
		targetLower := strings.ToLower(resolved)
		switch {
		case strings.Contains(typ, "/slidelayout"):
			graph.LayoutPath = resolved
		case strings.Contains(typ, "/notesslide"):
			graph.NotesPath = resolved
			if blocks := p.extractGenericTextBlocks(resolved, "speaker_notes"); len(blocks) > 0 {
				slide.Notes = blocks
			}
		case strings.Contains(typ, "/comments"):
			graph.CommentPaths = append(graph.CommentPaths, resolved)
			slide.Comments = append(slide.Comments, p.extractComments(resolved)...)
		case strings.Contains(typ, "/chart") || strings.HasSuffix(targetLower, ".xml") && strings.Contains(targetLower, "/charts/"):
			graph.ChartPaths = append(graph.ChartPaths, resolved)
			chart := p.extractChart(rel.ID, resolved)
			slide.Charts = append(slide.Charts, chart)
		case strings.Contains(typ, "/image") || isImagePath(targetLower):
			graph.MediaPaths = append(graph.MediaPaths, resolved)
			slide.Media = append(slide.Media, MediaRef{
				RelationshipID: rel.ID,
				XMLPath:        resolved,
				ContentType:    p.contentTypes.contentType(resolved),
			})
		case strings.Contains(typ, "/hyperlink") || strings.EqualFold(rel.TargetMode, "External"):
			link := Hyperlink{RelationshipID: rel.ID, Target: rel.Target, External: strings.EqualFold(rel.TargetMode, "External")}
			graph.Hyperlinks = append(graph.Hyperlinks, link)
			slide.Hyperlinks = append(slide.Hyperlinks, link)
		}
	}
	return slide, graph, nil
}

type shapeContext struct {
	kind             string
	depth            int
	id               string
	name             string
	description      string
	placeholder      string
	box              *Box
	paragraphs       []string
	currentParagraph strings.Builder
	capturingText    bool
}

func parseSlideText(xmlPath string, raw []byte) ([]TextBlock, error) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var blocks []TextBlock
	var stack []*shapeContext
	depth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			local := t.Name.Local
			if isShapeStart(local) {
				stack = append(stack, &shapeContext{kind: local, depth: depth})
			}
			current := topShape(stack)
			if current == nil {
				continue
			}
			switch local {
			case "cNvPr":
				current.id = attrValue(t, "id")
				current.name = attrValue(t, "name")
				current.description = firstNonEmpty(attrValue(t, "descr"), attrValue(t, "title"))
			case "ph":
				current.placeholder = attrValue(t, "type")
			case "off":
				if current.box == nil {
					current.box = &Box{}
				}
				current.box.X = parseIntAttr(t, "x")
				current.box.Y = parseIntAttr(t, "y")
			case "ext":
				if current.box == nil {
					current.box = &Box{}
				}
				current.box.CX = parseIntAttr(t, "cx")
				current.box.CY = parseIntAttr(t, "cy")
			case "t":
				current.capturingText = true
			}
		case xml.CharData:
			current := topShape(stack)
			if current != nil && current.capturingText {
				current.currentParagraph.WriteString(string(t))
			}
		case xml.EndElement:
			local := t.Name.Local
			current := topShape(stack)
			if current != nil {
				if local == "t" {
					current.capturingText = false
				}
				if local == "p" {
					flushParagraph(current)
				}
				if current.depth == depth && isShapeStart(local) {
					flushParagraph(current)
					if block, ok := current.textBlock(xmlPath); ok {
						blocks = append(blocks, block)
					}
					stack = stack[:len(stack)-1]
				}
			}
			depth--
		}
	}
	return blocks, nil
}

func isShapeStart(local string) bool {
	switch local {
	case "sp", "pic", "graphicFrame", "cxnSp":
		return true
	default:
		return false
	}
}

func topShape(stack []*shapeContext) *shapeContext {
	if len(stack) == 0 {
		return nil
	}
	return stack[len(stack)-1]
}

func flushParagraph(s *shapeContext) {
	if s == nil {
		return
	}
	text := strings.TrimSpace(s.currentParagraph.String())
	if text != "" {
		s.paragraphs = append(s.paragraphs, normalizeWhitespace(text))
	}
	s.currentParagraph.Reset()
}

func (s *shapeContext) textBlock(xmlPath string) (TextBlock, bool) {
	paras := compactStrings(s.paragraphs)
	if len(paras) == 0 {
		return TextBlock{}, false
	}
	source := "shape"
	if s.kind == "pic" {
		source = "picture"
	}
	if s.kind == "graphicFrame" {
		source = "graphic_frame"
	}
	return TextBlock{
		Text:             strings.Join(paras, "\n"),
		Source:           source,
		XMLPath:          xmlPath,
		ShapeID:          s.id,
		ShapeName:        s.name,
		ShapeDescription: s.description,
		PlaceholderType:  s.placeholder,
		BoundingBox:      cloneBox(s.box),
		Confidence:       0.99,
	}, true
}

func cloneBox(b *Box) *Box {
	if b == nil {
		return nil
	}
	c := *b
	return &c
}

func parseIntAttr(start xml.StartElement, name string) int64 {
	raw := attrValue(start, name)
	if raw == "" {
		return 0
	}
	v, _ := strconv.ParseInt(raw, 10, 64)
	return v
}

func attrValue(start xml.StartElement, local string) string {
	for _, attr := range start.Attr {
		if attr.Name.Local == local {
			return strings.TrimSpace(attr.Value)
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func chooseSlideTitle(blocks []TextBlock) string {
	for _, block := range blocks {
		ph := strings.ToLower(strings.TrimSpace(block.PlaceholderType))
		if ph == "title" || ph == "ctrtitle" || ph == "subTitle" || ph == "subtitle" {
			return firstLine(block.Text)
		}
	}
	for _, block := range blocks {
		name := strings.ToLower(block.ShapeName)
		if strings.Contains(name, "title") {
			return firstLine(block.Text)
		}
	}
	if len(blocks) > 0 {
		return firstLine(blocks[0].Text)
	}
	return ""
}

func firstLine(text string) string {
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func textBlockTexts(blocks []TextBlock) []string {
	out := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if text := strings.TrimSpace(block.Text); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func (p *packageReader) extractGenericTextBlocks(xmlPath string, source string) []TextBlock {
	raw, err := p.read(xmlPath)
	if err != nil {
		return nil
	}
	texts := textFromXML(raw, "t", "text")
	if len(texts) == 0 {
		return nil
	}
	return []TextBlock{{
		Text:       strings.Join(texts, "\n"),
		Source:     source,
		XMLPath:    xmlPath,
		Confidence: 0.98,
	}}
}

func (p *packageReader) extractComments(xmlPath string) []Comment {
	raw, err := p.read(xmlPath)
	if err != nil {
		return nil
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var comments []Comment
	var current *Comment
	var capture bool
	var buf strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "cm", "comment":
				current = &Comment{
					AuthorID:   firstNonEmpty(attrValue(t, "authorId"), attrValue(t, "author")),
					CreatedAt:  firstNonEmpty(attrValue(t, "dt"), attrValue(t, "created")),
					XMLPath:    xmlPath,
					Confidence: 0.95,
				}
			case "text", "t":
				capture = true
			}
		case xml.CharData:
			if capture {
				buf.WriteString(string(t))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "text", "t":
				capture = false
			case "cm", "comment":
				if current != nil {
					current.Text = normalizeWhitespace(buf.String())
					if current.Text != "" {
						comments = append(comments, *current)
					}
				}
				current = nil
				buf.Reset()
			}
		}
	}
	if len(comments) == 0 {
		texts := textFromXML(raw, "t", "text")
		if len(texts) > 0 {
			comments = append(comments, Comment{
				Text:       strings.Join(texts, "\n"),
				XMLPath:    xmlPath,
				Confidence: 0.85,
			})
		}
	}
	return comments
}

func (p *packageReader) extractChart(relID string, xmlPath string) Chart {
	chart := Chart{RelationshipID: relID, XMLPath: xmlPath}
	raw, err := p.read(xmlPath)
	if err != nil {
		return chart
	}
	chart.Text = compactStrings(textFromXML(raw, "t", "v"))
	chart.Series = parseChartSeries(raw)
	for _, text := range chart.Text {
		if strings.TrimSpace(text) != "" {
			chart.Title = strings.TrimSpace(text)
			break
		}
	}
	if len(chart.Series) > 0 && chart.Title == "" {
		chart.Title = chart.Series[0].Name
	}
	rels, err := p.relationshipsFor(xmlPath)
	if err == nil {
		ids := make([]string, 0, len(rels))
		for id := range rels {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			rel := rels[id]
			resolved := resolveRelationshipTarget(xmlPath, rel.Target)
			if strings.HasSuffix(strings.ToLower(resolved), ".xlsx") || strings.Contains(strings.ToLower(rel.Type), "/package") {
				chart.EmbeddedWorkbookPath = resolved
				break
			}
		}
	}
	return chart
}

func parseChartSeries(raw []byte) []ChartSeries {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var out []ChartSeries
	var current *ChartSeries
	var stack []string
	var capture bool
	var buf strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out
		}
		switch t := tok.(type) {
		case xml.StartElement:
			local := t.Name.Local
			stack = append(stack, local)
			if local == "ser" {
				current = &ChartSeries{}
			}
			if current != nil && local == "v" {
				capture = true
			}
		case xml.CharData:
			if capture {
				buf.WriteString(string(t))
			}
		case xml.EndElement:
			local := t.Name.Local
			if current != nil && local == "v" {
				value := normalizeWhitespace(buf.String())
				switch {
				case stackContains(stack, "tx"):
					current.Name = value
				case stackContains(stack, "cat"):
					current.Categories = append(current.Categories, value)
				case stackContains(stack, "val"):
					current.Values = append(current.Values, value)
				}
				capture = false
				buf.Reset()
			}
			if local == "ser" && current != nil {
				if current.Name != "" || len(current.Categories) > 0 || len(current.Values) > 0 {
					out = append(out, *current)
				}
				current = nil
			}
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return out
}

func stackContains(stack []string, local string) bool {
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == local {
			return true
		}
	}
	return false
}

func textFromXML(raw []byte, localNames ...string) []string {
	want := map[string]bool{}
	for _, local := range localNames {
		want[local] = true
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var out []string
	var capture bool
	var buf strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if want[t.Name.Local] {
				capture = true
			}
		case xml.CharData:
			if capture {
				buf.WriteString(string(t))
			}
		case xml.EndElement:
			if want[t.Name.Local] {
				if text := normalizeWhitespace(buf.String()); text != "" {
					out = append(out, text)
				}
				buf.Reset()
				capture = false
			}
		}
	}
	return compactStrings(out)
}

func isImagePath(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".bmp", ".tif", ".tiff", ".webp", ".emf", ".wmf", ".svg":
		return true
	default:
		return false
	}
}

func runMarkItDown(ctx context.Context, sourcePath string, outputDir string, timeout time.Duration) (string, Stage) {
	python, ok := findExecutable("python3", "python")
	if !ok {
		return "", Stage{Name: "markitdown_text", Status: StageSkipped, Detail: "python executable not found"}
	}
	runCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, python, "-m", "markitdown", sourcePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", Stage{
			Name:   "markitdown_text",
			Status: StageSkipped,
			Detail: "MarkItDown unavailable or failed; native Markdown output was still generated",
			Error:  trimCommandOutput(err, out),
		}
	}
	rel := "deck.markitdown.md"
	if err := writeFile(filepath.Join(outputDir, rel), out); err != nil {
		return "", Stage{Name: "markitdown_text", Status: StageFailed, Error: err.Error()}
	}
	return rel, Stage{Name: "markitdown_text", Status: StageCompleted, Detail: "Generated Markdown with MarkItDown", Outputs: []string{rel}}
}

func renderSlides(ctx context.Context, sourcePath string, outputDir string, timeout time.Duration) ([]string, Stage) {
	soffice, ok := findExecutable("soffice", "libreoffice")
	if !ok {
		return nil, Stage{Name: "visual_render", Status: StageSkipped, Detail: "LibreOffice/soffice not found"}
	}
	pdftoppm, ok := findExecutable("pdftoppm")
	if !ok {
		return nil, Stage{Name: "visual_render", Status: StageSkipped, Detail: "Poppler pdftoppm not found"}
	}
	renderDir := filepath.Join(outputDir, "rendered")
	tmpDir := filepath.Join(outputDir, ".render-tmp")
	if err := os.MkdirAll(renderDir, 0o700); err != nil {
		return nil, Stage{Name: "visual_render", Status: StageFailed, Error: err.Error()}
	}
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return nil, Stage{Name: "visual_render", Status: StageFailed, Error: err.Error()}
	}
	defer os.RemoveAll(tmpDir)

	runCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, soffice, "--headless", "--convert-to", "pdf", "--outdir", tmpDir, sourcePath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, Stage{Name: "visual_render", Status: StageFailed, Error: trimCommandOutput(err, out)}
	}
	pdfs, _ := filepath.Glob(filepath.Join(tmpDir, "*.pdf"))
	if len(pdfs) == 0 {
		return nil, Stage{Name: "visual_render", Status: StageFailed, Error: "LibreOffice did not produce a PDF"}
	}
	prefix := filepath.Join(tmpDir, "slide")
	cmd = exec.CommandContext(runCtx, pdftoppm, "-png", "-r", "150", pdfs[0], prefix)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, Stage{Name: "visual_render", Status: StageFailed, Error: trimCommandOutput(err, out)}
	}
	rendered, _ := filepath.Glob(filepath.Join(tmpDir, "slide-*.png"))
	sort.Slice(rendered, func(i, j int) bool { return renderedPageNumber(rendered[i]) < renderedPageNumber(rendered[j]) })
	if len(rendered) == 0 {
		return nil, Stage{Name: "visual_render", Status: StageFailed, Error: "pdftoppm did not produce slide images"}
	}
	relPaths := make([]string, 0, len(rendered))
	for i, src := range rendered {
		rel := path.Join("rendered", fmt.Sprintf("slide-%03d.png", i+1))
		dst := filepath.Join(outputDir, filepath.FromSlash(rel))
		if err := os.Rename(src, dst); err != nil {
			return nil, Stage{Name: "visual_render", Status: StageFailed, Error: err.Error()}
		}
		relPaths = append(relPaths, rel)
	}
	return relPaths, Stage{Name: "visual_render", Status: StageCompleted, Detail: fmt.Sprintf("Rendered %d slide images", len(relPaths)), Outputs: relPaths}
}

func renderedPageNumber(p string) int {
	base := filepath.Base(p)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	idx := strings.LastIndex(base, "-")
	if idx < 0 {
		return 0
	}
	n, _ := strconv.Atoi(base[idx+1:])
	return n
}

func runOCR(ctx context.Context, outputDir string, rendered map[int]string, deck *Deck, timeout time.Duration) ([]string, Stage) {
	tesseract, ok := findExecutable("tesseract")
	if !ok {
		return nil, Stage{Name: "ocr", Status: StageSkipped, Detail: "Tesseract OCR not found"}
	}
	runCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), timeout)
	defer cancel()
	outputs := make([]string, 0, len(rendered))
	for i := range deck.Slides {
		slideNo := deck.Slides[i].Number
		rel := rendered[slideNo]
		if rel == "" {
			continue
		}
		imagePath := filepath.Join(outputDir, filepath.FromSlash(rel))
		cmd := exec.CommandContext(runCtx, tesseract, filepath.Base(imagePath), "stdout", "--psm", "6")
		cmd.Dir = filepath.Dir(imagePath)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return outputs, Stage{Name: "ocr", Status: StageFailed, Error: trimCommandOutput(err, out), Outputs: outputs}
		}
		text := strings.TrimSpace(string(out))
		if text == "" {
			continue
		}
		deck.Slides[i].OCRText = text
		ocrRel := path.Join("ocr", fmt.Sprintf("slide-%03d.txt", slideNo))
		if err := writeFile(filepath.Join(outputDir, filepath.FromSlash(ocrRel)), []byte(text+"\n")); err != nil {
			return outputs, Stage{Name: "ocr", Status: StageFailed, Error: err.Error(), Outputs: outputs}
		}
		outputs = append(outputs, ocrRel)
	}
	if len(outputs) == 0 {
		return outputs, Stage{Name: "ocr", Status: StageSkipped, Detail: "OCR produced no text"}
	}
	return outputs, Stage{Name: "ocr", Status: StageCompleted, Detail: fmt.Sprintf("OCR extracted text from %d slides", len(outputs)), Outputs: outputs}
}

func findExecutable(names ...string) (string, bool) {
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil && p != "" {
			return p, true
		}
	}
	return "", false
}

func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func trimCommandOutput(err error, output []byte) string {
	msg := strings.TrimSpace(string(output))
	if msg == "" && err != nil {
		msg = err.Error()
	}
	if len(msg) > 500 {
		msg = msg[:500] + "..."
	}
	return msg
}

func (p *packageReader) writeRawXML(root string) ([]string, error) {
	names := make([]string, 0, len(p.files))
	for name := range p.files {
		if strings.HasSuffix(strings.ToLower(name), ".xml") || strings.HasSuffix(strings.ToLower(name), ".rels") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	relPaths := make([]string, 0, len(names))
	for _, name := range names {
		raw, err := p.read(name)
		if err != nil {
			return relPaths, err
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := writeFile(target, raw); err != nil {
			return relPaths, err
		}
		relPaths = append(relPaths, path.Join("raw", "ooxml", name))
	}
	return relPaths, nil
}

func renderMarkdown(deck Deck) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(firstNonEmpty(deck.FileName, "Presentation"))
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("Slides: %d\n\n", len(deck.Slides)))
	for _, slide := range deck.Slides {
		title := strings.TrimSpace(slide.Title)
		if title == "" {
			title = fmt.Sprintf("Slide %d", slide.Number)
		}
		b.WriteString(fmt.Sprintf("## Slide %d: %s\n\n", slide.Number, title))
		if slide.Hidden {
			b.WriteString("_Hidden slide._\n\n")
		}
		if slide.XMLPath != "" {
			b.WriteString(fmt.Sprintf("Source: `%s`\n\n", slide.XMLPath))
		}
		writeTextBlocksMarkdown(&b, "Text", slide.Text)
		writeTextBlocksMarkdown(&b, "Speaker Notes", slide.Notes)
		if len(slide.Comments) > 0 {
			b.WriteString("### Comments\n\n")
			for _, comment := range slide.Comments {
				b.WriteString("- ")
				b.WriteString(comment.Text)
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
		if len(slide.Charts) > 0 {
			b.WriteString("### Charts\n\n")
			for _, chart := range slide.Charts {
				label := firstNonEmpty(chart.Title, chart.XMLPath)
				b.WriteString("- ")
				b.WriteString(label)
				if chart.EmbeddedWorkbookPath != "" {
					b.WriteString(" (embedded workbook: `")
					b.WriteString(chart.EmbeddedWorkbookPath)
					b.WriteString("`)")
				}
				b.WriteString("\n")
				for _, series := range chart.Series {
					if series.Name != "" {
						b.WriteString("  - Series: ")
						b.WriteString(series.Name)
						b.WriteString("\n")
					}
					for i := 0; i < len(series.Categories) || i < len(series.Values); i++ {
						cat := valueAt(series.Categories, i)
						val := valueAt(series.Values, i)
						if cat != "" || val != "" {
							b.WriteString("    - ")
							b.WriteString(cat)
							if cat != "" && val != "" {
								b.WriteString(": ")
							}
							b.WriteString(val)
							b.WriteString("\n")
						}
					}
				}
			}
			b.WriteString("\n")
		}
		if len(slide.Media) > 0 {
			b.WriteString("### Media\n\n")
			for _, media := range slide.Media {
				b.WriteString("- `")
				b.WriteString(media.XMLPath)
				b.WriteString("`")
				if media.ContentType != "" {
					b.WriteString(" ")
					b.WriteString(media.ContentType)
				}
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
		if slide.OCRText != "" {
			b.WriteString("### OCR Text\n\n")
			b.WriteString(slide.OCRText)
			b.WriteString("\n\n")
		}
	}
	return b.String()
}

func writeTextBlocksMarkdown(b *strings.Builder, title string, blocks []TextBlock) {
	if len(blocks) == 0 {
		return
	}
	b.WriteString("### ")
	b.WriteString(title)
	b.WriteString("\n\n")
	for _, block := range blocks {
		for _, line := range strings.Split(block.Text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			b.WriteString("- ")
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
}

func deckPlainText(deck Deck) string {
	var parts []string
	for _, slide := range deck.Slides {
		if slide.Title != "" {
			parts = append(parts, slide.Title)
		}
		parts = append(parts, textBlockTexts(slide.Text)...)
		parts = append(parts, textBlockTexts(slide.Notes)...)
		for _, comment := range slide.Comments {
			parts = append(parts, comment.Text)
		}
		for _, chart := range slide.Charts {
			parts = append(parts, chart.Title)
			parts = append(parts, chart.Text...)
		}
		if slide.OCRText != "" {
			parts = append(parts, slide.OCRText)
		}
	}
	return strings.Join(compactStrings(parts), "\n")
}

func capabilities(deck Deck) map[string]bool {
	hasNotes := false
	hasComments := false
	hasCharts := false
	hasMedia := false
	hasOCR := false
	hasRendered := false
	hasText := false
	for _, slide := range deck.Slides {
		hasText = hasText || len(slide.Text) > 0
		hasNotes = hasNotes || len(slide.Notes) > 0
		hasComments = hasComments || len(slide.Comments) > 0
		hasCharts = hasCharts || len(slide.Charts) > 0
		hasMedia = hasMedia || len(slide.Media) > 0
		hasOCR = hasOCR || strings.TrimSpace(slide.OCRText) != ""
		hasRendered = hasRendered || strings.TrimSpace(slide.RenderPath) != ""
	}
	return map[string]bool{
		"hasStructuredText": hasText,
		"hasMarkdown":       true,
		"hasSlideJSON":      true,
		"hasNotes":          hasNotes,
		"hasComments":       hasComments,
		"hasCharts":         hasCharts,
		"hasMedia":          hasMedia,
		"hasRenderedSlides": hasRendered,
		"hasOCR":            hasOCR,
	}
}

func valueAt(values []string, idx int) string {
	if idx < 0 || idx >= len(values) {
		return ""
	}
	return values[idx]
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := normalizeWhitespace(value)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func writeJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(raw, '\n'))
}

func writeFile(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

type manifestBuilder struct {
	manifest Manifest
}

func (b *manifestBuilder) stage(stage Stage) {
	b.manifest.Stages = append(b.manifest.Stages, stage)
	if stage.Status == StageFailed && b.manifest.Status == StatusComplete {
		b.manifest.Status = StatusPartial
	}
}

func (b *manifestBuilder) optionalStage(stage Stage) {
	b.manifest.Stages = append(b.manifest.Stages, stage)
}

func (b *manifestBuilder) output(key string, value any) {
	if b.manifest.Outputs == nil {
		b.manifest.Outputs = map[string]any{}
	}
	b.manifest.Outputs[key] = value
}

func (b *manifestBuilder) finish() Manifest {
	if b.manifest.Status == "" {
		b.manifest.Status = StatusComplete
	}
	if len(b.manifest.Stages) > 0 {
		allFailed := true
		for _, stage := range b.manifest.Stages {
			if stage.Status != StageFailed {
				allFailed = false
				break
			}
		}
		if allFailed {
			b.manifest.Status = StatusFailed
		}
	}
	return b.manifest
}

func ProjectExtractionKey(relPath string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(filepath.ToSlash(relPath))))
	return hex.EncodeToString(sum[:])[:24]
}
