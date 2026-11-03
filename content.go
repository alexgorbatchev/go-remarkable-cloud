package cloud

import "encoding/json"

// Register is a tablet value with its conflict-resolution timestamp. Extra
// retains fields unknown to this version of the library without numeric loss.
type Register[T int | string] struct {
	Timestamp string                     `json:"timestamp"`
	Value     T                          `json:"value"`
	Extra     map[string]json.RawMessage `json:"-"`
	jsonState objectState
}

// CPage is a lossless page record. Idx orders the page, Redir selects a source
// background, and Deleted marks removal; each is a timestamped register.
type CPage struct {
	ID        string                     `json:"id"`
	Idx       *Register[string]          `json:"idx"`
	Template  *Register[string]          `json:"template"`
	Redir     *Register[int]             `json:"redir"`
	Deleted   *Register[int]             `json:"deleted"`
	Extra     map[string]json.RawMessage `json:"-"`
	jsonState objectState
}

// CPages retains page records and the original-page-count and last-opened
// registers. Other fields, including uuids, remain in Extra.
type CPages struct {
	Pages      []CPage                    `json:"pages"`
	Original   *Register[int]             `json:"original"`
	LastOpened *Register[string]          `json:"lastOpened"`
	Extra      map[string]json.RawMessage `json:"-"`
	jsonState  objectState
}

// DocumentContent is a lossless editable .content model. Unknown fields at
// every model level are preserved in Extra. Decoding then encoding preserves
// absent fields, nulls, zero values, and JSON numbers without float conversion.
type DocumentContent struct {
	CPages            CPages                     `json:"cPages"`
	FileType          string                     `json:"fileType"`
	FormatVersion     int                        `json:"formatVersion"`
	PageCount         int                        `json:"pageCount"`
	Pages             []string                   `json:"pages"`
	Orientation       string                     `json:"orientation"`
	CoverPageNumber   int                        `json:"coverPageNumber"`
	OriginalPageCount int                        `json:"originalPageCount"`
	Extra             map[string]json.RawMessage `json:"-"`
	jsonState         objectState
}

func (r *Register[T]) UnmarshalJSON(data []byte) error {
	type plain Register[T]
	var value plain
	state, extra, err := decodeObject(data, &value)
	if err != nil {
		return err
	}
	*r = Register[T](value)
	r.jsonState, r.Extra = state, extra
	return nil
}

func (r Register[T]) MarshalJSON() ([]byte, error) {
	type plain Register[T]
	return encodeObject(plain(r), r.Extra, r.jsonState)
}

func (p *CPage) UnmarshalJSON(data []byte) error {
	type plain CPage
	var value plain
	state, extra, err := decodeObject(data, &value)
	if err != nil {
		return err
	}
	*p = CPage(value)
	p.jsonState, p.Extra = state, extra
	return nil
}

func (p CPage) MarshalJSON() ([]byte, error) {
	type plain CPage
	return encodeObject(plain(p), p.Extra, p.jsonState)
}

func (p *CPages) UnmarshalJSON(data []byte) error {
	type plain CPages
	var value plain
	state, extra, err := decodeObject(data, &value)
	if err != nil {
		return err
	}
	*p = CPages(value)
	p.jsonState, p.Extra = state, extra
	return nil
}

func (p CPages) MarshalJSON() ([]byte, error) {
	type plain CPages
	return encodeObject(plain(p), p.Extra, p.jsonState)
}

func (dc *DocumentContent) UnmarshalJSON(data []byte) error {
	type plain DocumentContent
	var value plain
	state, extra, err := decodeObject(data, &value)
	if err != nil {
		return err
	}
	*dc = DocumentContent(value)
	dc.jsonState, dc.Extra = state, extra
	return nil
}

func (dc DocumentContent) MarshalJSON() ([]byte, error) {
	type plain DocumentContent
	return encodeObject(plain(dc), dc.Extra, dc.jsonState)
}
