package slides

import (
	"encoding/json"
	"fmt"
	"math"
)

// emuPerPt converts the Slides API's English Metric Units to points.
const emuPerPt = 12700.0

// Deck is the part of a Slides API Presentation resource
// (presentations.get) a preview draws. Every field the API leaves out reads
// as its zero value, which is the API's own convention: an absent rgbColor
// channel is 0, an absent propertyState is RENDERED.
type Deck struct {
	PageSize Size   `json:"pageSize"`
	Slides   []Page `json:"slides"`
	Layouts  []Page `json:"layouts"`
	Masters  []Page `json:"masters"`
}

type Page struct {
	ObjectID        string    `json:"objectId"`
	PageElements    []Element `json:"pageElements"`
	PageProperties  PageProps `json:"pageProperties"`
	SlideProperties struct {
		LayoutObjectID string `json:"layoutObjectId"`
		MasterObjectID string `json:"masterObjectId"`
	} `json:"slideProperties"`
	LayoutProperties struct {
		MasterObjectID string `json:"masterObjectId"`
	} `json:"layoutProperties"`
}

type PageProps struct {
	PageBackgroundFill struct {
		PropertyState        string `json:"propertyState"`
		SolidFill            *Solid `json:"solidFill"`
		StretchedPictureFill *struct {
			ContentURL string `json:"contentUrl"`
		} `json:"stretchedPictureFill"`
	} `json:"pageBackgroundFill"`
	ColorScheme struct {
		Colors []struct {
			Type  string `json:"type"`
			Color RGB    `json:"color"`
		} `json:"colors"`
	} `json:"colorScheme"`
}

// Dim is a Slides Dimension: a magnitude in EMU or PT.
type Dim struct {
	Magnitude float64 `json:"magnitude"`
	Unit      string  `json:"unit"`
}

// pt is d in points; or, when d is absent, def.
func (d *Dim) pt(def float64) float64 {
	if d == nil {
		return def
	}
	if d.Unit == "EMU" {
		return d.Magnitude / emuPerPt
	}
	return d.Magnitude
}

type Size struct {
	Width  *Dim `json:"width"`
	Height *Dim `json:"height"`
}

// Transform is an AffineTransform. An absent scale reads as 1, not 0: a
// zero scale would collapse the element, which Slides never sends.
type Transform struct {
	ScaleX     *float64 `json:"scaleX"`
	ScaleY     *float64 `json:"scaleY"`
	ShearX     float64  `json:"shearX"`
	ShearY     float64  `json:"shearY"`
	TranslateX float64  `json:"translateX"`
	TranslateY float64  `json:"translateY"`
	Unit       string   `json:"unit"`
}

// affine is a 2×3 matrix (a b c d e f): x' = a·x + c·y + e, y' = b·x + d·y + f.
type affine [6]float64

var identity = affine{1, 0, 0, 1, 0, 0}

func (t *Transform) affine() affine {
	if t == nil {
		return identity
	}
	one := func(p *float64) float64 {
		if p == nil {
			return 1
		}
		return *p
	}
	k := 1 / emuPerPt
	if t.Unit == "PT" {
		k = 1
	}
	return affine{one(t.ScaleX), t.ShearY, t.ShearX, one(t.ScaleY), t.TranslateX * k, t.TranslateY * k}
}

func (m affine) times(n affine) affine {
	return affine{
		m[0]*n[0] + m[2]*n[1], m[1]*n[0] + m[3]*n[1],
		m[0]*n[2] + m[2]*n[3], m[1]*n[2] + m[3]*n[3],
		m[0]*n[4] + m[2]*n[5] + m[4], m[1]*n[4] + m[3]*n[5] + m[5],
	}
}

type Element struct {
	ObjectID     string     `json:"objectId"`
	Size         Size       `json:"size"`
	Transform    *Transform `json:"transform"`
	Shape        *Shape     `json:"shape"`
	Image        *Image     `json:"image"`
	ElementGroup *struct {
		Children []Element `json:"children"`
	} `json:"elementGroup"`
	// The kinds a preview does not draw, held only to say so.
	Line             json.RawMessage `json:"line"`
	Table            json.RawMessage `json:"table"`
	Video            json.RawMessage `json:"video"`
	SheetsChart      json.RawMessage `json:"sheetsChart"`
	WordArt          json.RawMessage `json:"wordArt"`
	SpeakerSpotlight json.RawMessage `json:"speakerSpotlight"`
}

// undrawn names the kind of an element the preview skips, or "".
func (e Element) undrawn() string {
	for _, k := range []struct {
		name string
		raw  json.RawMessage
	}{{"line", e.Line}, {"table", e.Table}, {"video", e.Video}, {"chart", e.SheetsChart}, {"word art", e.WordArt}, {"speaker spotlight", e.SpeakerSpotlight}} {
		if k.raw != nil {
			return k.name
		}
	}
	return ""
}

type Shape struct {
	ShapeType       string     `json:"shapeType"`
	Text            *Text      `json:"text"`
	ShapeProperties ShapeProps `json:"shapeProperties"`
	Placeholder     *struct {
		ParentObjectID string `json:"parentObjectId"`
	} `json:"placeholder"`
}

type ShapeProps struct {
	ShapeBackgroundFill struct {
		PropertyState string `json:"propertyState"`
		SolidFill     *Solid `json:"solidFill"`
	} `json:"shapeBackgroundFill"`
	Outline          Outline `json:"outline"`
	ContentAlignment string  `json:"contentAlignment"`
}

type Outline struct {
	PropertyState string `json:"propertyState"`
	OutlineFill   struct {
		SolidFill *Solid `json:"solidFill"`
	} `json:"outlineFill"`
	Weight *Dim `json:"weight"`
}

type Image struct {
	ContentURL      string `json:"contentUrl"`
	ImageProperties struct {
		Outline Outline `json:"outline"`
	} `json:"imageProperties"`
}

type Solid struct {
	Color *OpaqueColor `json:"color"`
	Alpha *float64     `json:"alpha"`
}

type OpaqueColor struct {
	RGBColor   *RGB   `json:"rgbColor"`
	ThemeColor string `json:"themeColor"`
}

type RGB struct {
	Red   float64 `json:"red"`
	Green float64 `json:"green"`
	Blue  float64 `json:"blue"`
}

func (c RGB) css(alpha float64) string {
	b := func(v float64) int { return int(math.Round(v * 255)) }
	if alpha < 1 {
		return fmt.Sprintf("rgba(%d,%d,%d,%g)", b(c.Red), b(c.Green), b(c.Blue), alpha)
	}
	return fmt.Sprintf("rgb(%d,%d,%d)", b(c.Red), b(c.Green), b(c.Blue))
}

type Text struct {
	TextElements []struct {
		ParagraphMarker *struct {
			Style  ParaStyle `json:"style"`
			Bullet *struct {
				NestingLevel int `json:"nestingLevel"`
			} `json:"bullet"`
		} `json:"paragraphMarker"`
		TextRun *struct {
			Content string    `json:"content"`
			Style   TextStyle `json:"style"`
		} `json:"textRun"`
	} `json:"textElements"`
}

type ParaStyle struct {
	LineSpacing *float64 `json:"lineSpacing"`
	Alignment   string   `json:"alignment"`
	SpaceAbove  *Dim     `json:"spaceAbove"`
	SpaceBelow  *Dim     `json:"spaceBelow"`
	IndentStart *Dim     `json:"indentStart"`
}

// over returns p with every field base sets and p does not taken from base.
func (p ParaStyle) over(base ParaStyle) ParaStyle {
	if p.LineSpacing == nil {
		p.LineSpacing = base.LineSpacing
	}
	if p.Alignment == "" {
		p.Alignment = base.Alignment
	}
	if p.SpaceAbove == nil {
		p.SpaceAbove = base.SpaceAbove
	}
	if p.SpaceBelow == nil {
		p.SpaceBelow = base.SpaceBelow
	}
	if p.IndentStart == nil {
		p.IndentStart = base.IndentStart
	}
	return p
}

type TextStyle struct {
	FontFamily         string `json:"fontFamily"`
	WeightedFontFamily *struct {
		FontFamily string `json:"fontFamily"`
		Weight     int    `json:"weight"`
	} `json:"weightedFontFamily"`
	FontSize        *Dim  `json:"fontSize"`
	Bold            *bool `json:"bold"`
	Italic          *bool `json:"italic"`
	Underline       *bool `json:"underline"`
	Strikethrough   *bool `json:"strikethrough"`
	ForegroundColor *struct {
		OpaqueColor *OpaqueColor `json:"opaqueColor"`
	} `json:"foregroundColor"`
}

// over returns s with every field base sets and s does not taken from base.
func (s TextStyle) over(base TextStyle) TextStyle {
	if s.FontFamily == "" && s.WeightedFontFamily == nil {
		s.FontFamily, s.WeightedFontFamily = base.FontFamily, base.WeightedFontFamily
	}
	if s.FontSize == nil {
		s.FontSize = base.FontSize
	}
	for _, f := range [][2]**bool{{&s.Bold, &base.Bold}, {&s.Italic, &base.Italic}, {&s.Underline, &base.Underline}, {&s.Strikethrough, &base.Strikethrough}} {
		if *f[0] == nil {
			*f[0] = *f[1]
		}
	}
	if s.ForegroundColor == nil || s.ForegroundColor.OpaqueColor == nil {
		s.ForegroundColor = base.ForegroundColor
	}
	return s
}

// family is the face the run names; Slides' default is Arial.
func (s TextStyle) family() string {
	if s.WeightedFontFamily != nil && s.WeightedFontFamily.FontFamily != "" {
		return s.WeightedFontFamily.FontFamily
	}
	if s.FontFamily != "" {
		return s.FontFamily
	}
	return "Arial"
}

// weight is the weight Slides draws the run at, from its weighted family's
// weight and its bold flag, by the rule the Slides API documents for
// WeightedFontFamily: bold lifts a weight below 400 to 400, below 700 to
// 700, and leaves 700 and above alone.
func (s TextStyle) weight() int {
	w := 400
	if s.WeightedFontFamily != nil && s.WeightedFontFamily.Weight > 0 {
		w = s.WeightedFontFamily.Weight
	}
	if s.Bold != nil && *s.Bold {
		switch {
		case w < 400:
			return 400
		case w < 700:
			return 700
		}
	}
	return w
}

func (s TextStyle) italic() bool { return s.Italic != nil && *s.Italic }
