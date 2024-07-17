package main

// This program generates all of the possible key types that we use
// RSA public/private keys, ECDSA private/public keys, and symmetric keys
//
// Each share the same standard header section, but have their own
// header fields

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/lestrrat-go/codegen"
)

func main() {
	if err := _main(); err != nil {
		log.Printf("%s", err)
		os.Exit(1)
	}
}

func yaml2json(fn string) ([]byte, error) {
	in, err := os.Open(fn)
	if err != nil {
		return nil, fmt.Errorf(`failed to open %q: %w`, fn, err)
	}
	defer in.Close()

	var v interface{}
	if err := yaml.NewDecoder(in).Decode(&v); err != nil {
		return nil, fmt.Errorf(`failed to decode %q: %w`, fn, err)
	}

	return json.Marshal(v)
}

type KeyType struct {
	Filename string            `json:"filename"`
	Prefix   string            `json:"prefix"`
	KeyType  string            `json:"key_type"`
	Objects  []*codegen.Object `json:"objects"`
}

func _main() error {
	codegen.RegisterZeroVal(`jwa.EllipticCurveAlgorithm`, `jwa.InvalidEllipticCurve`)
	codegen.RegisterZeroVal(`jwa.KeyType`, `jwa.InvalidKeyType`)
	codegen.RegisterZeroVal(`jwa.KeyAlgorithm`, `jwa.InvalidKeyAlgorithm("")`)

	var objectsFile = flag.String("objects", "objects.yml", "")
	flag.Parse()
	jsonSrc, err := yaml2json(*objectsFile)
	if err != nil {
		return err
	}

	var def struct {
		StdFields codegen.FieldList `json:"std_fields"`
		KeyTypes  []*KeyType        `json:"key_types"`
	}
	if err := json.NewDecoder(bytes.NewReader(jsonSrc)).Decode(&def); err != nil {
		return fmt.Errorf(`failed to decode %q: %w`, *objectsFile, err)
	}

	for _, kt := range def.KeyTypes {
		for _, object := range kt.Objects {
			for _, f := range def.StdFields {
				object.AddField(f)
			}

			object.Organize()
		}
	}

	if err := generateGenericHeaders(def.StdFields); err != nil {
		return err
	}
	for _, kt := range def.KeyTypes {
		if err := generateKeyType(kt); err != nil {
			return fmt.Errorf(`failed to generate key type %s: %w`, kt.Prefix, err)
		}
	}

	return nil
}

func IsPointer(f codegen.Field) bool {
	return strings.HasPrefix(f.Type(), `*`)
}

func PointerElem(f codegen.Field) string {
	return strings.TrimPrefix(f.Type(), `*`)
}
func fieldStorageType(s string) string {
	if fieldStorageTypeIsIndirect(s) {
		return `*` + s
	}
	return s
}

func fieldStorageTypeIsIndirect(s string) bool {
	return s == "KeyOperationList" || !(strings.HasPrefix(s, `*`) || strings.HasPrefix(s, `[]`) || strings.HasSuffix(s, `List`))
}

type Constant struct {
	Name  string
	Value string
}

func generateKeyType(kt *KeyType) error {
	var buf bytes.Buffer
	o := codegen.NewOutput(&buf)
	o.L("// Code generated by tools/cmd/genjwk/main.go. DO NOT EDIT.")
	o.LL("package jwk")

	// Find unique field key names to create constants
	var constants []Constant
	seen := make(map[string]struct{})
	for _, obj := range kt.Objects {
		for _, f := range obj.Fields() {
			if f.Bool(`is_std`) {
				continue
			}
			n := f.Name(true)
			if _, ok := seen[n]; ok {
				continue
			}
			seen[n] = struct{}{}

			constants = append(constants, Constant{Name: kt.Prefix + n + "Key", Value: f.JSON()})
		}
	}

	sort.Slice(constants, func(i, j int) bool {
		return constants[i].Name < constants[j].Name
	})
	o.LL("const (")
	for _, c := range constants {
		o.L("%s = %q", c.Name, c.Value)
	}
	o.L(")")

	for _, obj := range kt.Objects {
		if err := generateObject(o, kt, obj); err != nil {
			return fmt.Errorf(`failed to generate object %s: %w`, obj.Name(true), err)
		}
	}

	if err := o.WriteFile(kt.Filename, codegen.WithFormatCode(true)); err != nil {
		if cfe, ok := err.(codegen.CodeFormatError); ok {
			fmt.Fprint(os.Stderr, cfe.Source())
		}
		return fmt.Errorf(`failed to write to %s: %w`, kt.Filename, err)
	}
	return nil
}

func generateObject(o *codegen.Output, kt *KeyType, obj *codegen.Object) error {
	ifName := kt.Prefix + obj.Name(true)
	if v := obj.String(`interface`); v != "" {
		ifName = v
	}
	objName := obj.Name(true)
	structName := strings.ToLower(kt.Prefix) + objName
	if v := obj.String(`struct_name`); v != "" {
		structName = v
	}

	o.LL("type %s interface {", ifName)
	o.L("Key")
	o.L("FromRaw(%s) error", obj.MustString(`raw_key_type`))
	for _, f := range obj.Fields() {
		if f.Bool(`is_std`) {
			continue
		}
		o.L("%s() %s", f.GetterMethod(true), f.Type())
	}
	o.L("}")

	o.LL("type %s struct {", structName)
	for _, f := range obj.Fields() {
		o.L("%s %s", f.Name(false), fieldStorageType(f.Type()))
		if c := f.Comment(); len(c) > 0 {
			o.R(" // %s", c)
		}
	}
	o.L("privateParams map[string]interface{}")
	o.L("mu *sync.RWMutex")
	o.L("dc json.DecodeCtx")
	o.L("}")

	o.LL(`var _ %s = &%s{}`, ifName, structName)
	o.L(`var _ Key = &%s{}`, structName)

	o.LL("func new%s() *%s {", ifName, structName)
	o.L("return &%s{", structName)
	o.L("mu: &sync.RWMutex{},")
	o.L("privateParams: make(map[string]interface{}),")
	o.L("}")
	o.L("}")

	o.LL("func (h %s) KeyType() jwa.KeyType {", structName)
	o.L("return %s", kt.KeyType)
	o.L("}")

	if objName == "PublicKey" || objName == "PrivateKey" {
		o.LL("func (h %s) IsPrivate() bool {", structName)
		o.L("return %s", fmt.Sprint(objName == "PrivateKey"))
		o.L("}")
	}

	for _, f := range obj.Fields() {
		o.LL("func (h *%s) %s() ", structName, f.GetterMethod(true))
		if v := f.String(`getter_return_value`); v != "" {
			o.R("%s", v)
		} else if IsPointer(f) && f.Bool(`noDeref`) {
			o.R("%s", f.Type())
		} else {
			o.R("%s", PointerElem(f))
		}
		o.R(" {")

		if f.Bool(`hasGet`) {
			o.L("if h.%s != nil {", f.Name(false))
			o.L("return h.%s.Get()", f.Name(false))
			o.L("}")
			o.L("return %s", codegen.ZeroVal(PointerElem(f)))
		} else if !IsPointer(f) {
			if fieldStorageTypeIsIndirect(f.Type()) {
				o.L("if h.%s != nil {", f.Name(false))
				o.L("return *(h.%s)", f.Name(false))
				o.L("}")
				o.L("return %s", codegen.ZeroVal(PointerElem(f)))
			} else {
				o.L("return h.%s", f.Name(false))
			}
		} else {
			o.L(`return h.%s`, f.Name(false))
		}
		o.L("}") // func (h *stdHeaders) %s() %s
	}

	o.LL("func (h *%s) makePairs() []*HeaderPair {", structName)
	o.L("h.mu.RLock()")
	o.L("defer h.mu.RUnlock()")

	// NOTE: building up an array is *slow*?
	o.LL("var pairs []*HeaderPair")
	o.L("pairs = append(pairs, &HeaderPair{Key: \"kty\", Value: %s})", kt.KeyType)
	for _, f := range obj.Fields() {
		var keyName string
		if f.Bool(`is_std`) {
			keyName = f.Name(true) + "Key"
		} else {
			keyName = kt.Prefix + f.Name(true) + "Key"
		}
		o.L("if h.%s != nil {", f.Name(false))
		if fieldStorageTypeIsIndirect(f.Type()) {
			o.L("pairs = append(pairs, &HeaderPair{Key: %s, Value: *(h.%s)})", keyName, f.Name(false))
		} else {
			o.L("pairs = append(pairs, &HeaderPair{Key: %s, Value: h.%s})", keyName, f.Name(false))
		}
		o.L("}")
	}
	o.L("for k, v := range h.privateParams {")
	o.L("pairs = append(pairs, &HeaderPair{Key: k, Value: v})")
	o.L("}")
	o.L("return pairs")
	o.L("}") // end of (h *stdHeaders) makePairs(...)

	o.LL("func (h *%s) PrivateParams() map[string]interface{} {", structName)
	o.L("return h.privateParams")
	o.L("}")

	o.LL("func (h *%s) Get(name string) (interface{}, bool) {", structName)
	o.L("h.mu.RLock()")
	o.L("defer h.mu.RUnlock()")
	o.L("switch name {")
	o.L("case KeyTypeKey:")
	o.L("return h.KeyType(), true")
	for _, f := range obj.Fields() {
		if f.Bool(`is_std`) {
			o.L("case %sKey:", f.Name(true))
		} else {
			o.L("case %s%sKey:", kt.Prefix, f.Name(true))
		}

		o.L("if h.%s == nil {", f.Name(false))
		o.L("return nil, false")
		o.L("}")
		if f.Bool(`hasGet`) {
			o.L("return h.%s.Get(), true", f.Name(false))
		} else if fieldStorageTypeIsIndirect(f.Type()) {
			o.L("return *(h.%s), true", f.Name(false))
		} else {
			o.L("return h.%s, true", f.Name(false))
		}
	}
	o.L("default:")
	o.L("v, ok := h.privateParams[name]")
	o.L("return v, ok")
	o.L("}") // end switch name
	o.L("}") // func (h *%s) Get(name string) (interface{}, bool)

	o.LL("func (h *%s) Set(name string, value interface{}) error {", structName)
	o.L("h.mu.Lock()")
	o.L("defer h.mu.Unlock()")
	o.L("return h.setNoLock(name, value)")
	o.L(`}`)

	o.LL("func (h *%s) setNoLock(name string, value interface{}) error {", structName)
	o.L("switch name {")
	o.L("case \"kty\":")
	o.L("return nil") // This is not great, but we just ignore it
	for _, f := range obj.Fields() {
		var keyName string
		if f.Bool(`is_std`) {
			keyName = f.Name(true) + "Key"
		} else {
			keyName = kt.Prefix + f.Name(true) + "Key"
		}
		o.L("case %s:", keyName)
		if f.Name(false) == `algorithm` {
			o.L("switch v := value.(type) {")
			o.L("case string, jwa.SignatureAlgorithm, jwa.ContentEncryptionAlgorithm:")
			o.L("var tmp = jwa.KeyAlgorithmFrom(v)")
			o.L("h.algorithm = &tmp")
			o.L("case fmt.Stringer:")
			o.L("s := v.String()")
			o.L("var tmp = jwa.KeyAlgorithmFrom(s)")
			o.L("h.algorithm = &tmp")
			o.L("default:")
			o.L("return fmt.Errorf(`invalid type for %%s key: %%T`, %s, value)", keyName)
			o.L("}")
			o.L("return nil")
		} else if f.Name(false) == `keyUsage` {
			o.L("switch v := value.(type) {")
			o.L("case KeyUsageType:")
			o.L("switch v {")
			o.L("case ForSignature, ForEncryption:")
			o.L("tmp := v.String()")
			o.L("h.keyUsage = &tmp")
			o.L("default:")
			o.L("return fmt.Errorf(`invalid key usage type %%s`, v)")
			o.L("}")
			o.L("case string:")
			o.L("h.keyUsage = &v")
			o.L("default:")
			o.L("return fmt.Errorf(`invalid key usage type %%s`, v)")
			o.L("}")
		} else if f.Bool(`hasAccept`) {
			o.L("var acceptor %s", f.Type())
			o.L("if err := acceptor.Accept(value); err != nil {")
			o.L("return fmt.Errorf(`invalid value for %%s key: %%w`, %s, err)", keyName)
			o.L("}") // end if err := h.%s.Accept(value)
			if fieldStorageTypeIsIndirect(f.Type()) {
				o.L("h.%s = &acceptor", f.Name(false))
			} else {
				o.L("h.%s = acceptor", f.Name(false))
			}
			o.L("return nil")
		} else {
			o.L("if v, ok := value.(%s); ok {", f.Type())
			if fieldStorageTypeIsIndirect(f.Type()) {
				o.L("h.%s = &v", f.Name(false))
			} else {
				o.L("h.%s = v", f.Name(false))
			}
			o.L("return nil")
			o.L("}") // end if v, ok := value.(%s)
			o.L("return fmt.Errorf(`invalid value for %%s key: %%T`, %s, value)", keyName)
		}
	}
	o.L("default:")
	o.L("if h.privateParams == nil {")
	o.L("h.privateParams = map[string]interface{}{}")
	o.L("}") // end if h.privateParams == nil
	o.L("h.privateParams[name] = value")
	o.L("}") // end switch name
	o.L("return nil")
	o.L("}") // end func (h *%s) Set(name string, value interface{})

	o.LL("func (k *%s) Remove(key string) error {", structName)
	o.L("k.mu.Lock()")
	o.L("defer k.mu.Unlock()")
	o.L("switch key {")
	for _, f := range obj.Fields() {
		var keyName string
		if f.Bool(`is_std`) {
			keyName = f.Name(true) + "Key"
		} else {
			keyName = kt.Prefix + f.Name(true) + "Key"
		}
		o.L("case %s:", keyName)
		o.L("k.%s = nil", f.Name(false))
	}
	o.L("default:")
	o.L("delete(k.privateParams, key)")
	o.L("}")
	o.L("return nil") // currently unused, but who knows
	o.L("}")

	o.LL("func (k *%s) Clone() (Key, error) {", structName)
	o.L("return cloneKey(k)")
	o.L("}")

	o.LL("func (k *%s) DecodeCtx() json.DecodeCtx {", structName)
	o.L("k.mu.RLock()")
	o.L("defer k.mu.RUnlock()")
	o.L("return k.dc")
	o.L("}")

	o.LL("func (k *%s) SetDecodeCtx(dc json.DecodeCtx) {", structName)
	o.L("k.mu.Lock()")
	o.L("defer k.mu.Unlock()")
	o.L("k.dc = dc")
	o.L("}")

	o.LL("func (h *%s) UnmarshalJSON(buf []byte) error {", structName)
	o.L(`h.mu.Lock()`)
	o.L(`defer h.mu.Unlock()`)
	for _, f := range obj.Fields() {
		o.L("h.%s = nil", f.Name(false))
	}

	o.L("dec := json.NewDecoder(bytes.NewReader(buf))")
	o.L("LOOP:")
	o.L("for {")
	o.L("tok, err := dec.Token()")
	o.L("if err != nil {")
	o.L("return fmt.Errorf(`error reading token: %%w`, err)")
	o.L("}")
	o.L("switch tok := tok.(type) {")
	o.L("case json.Delim:")
	o.L("// Assuming we're doing everything correctly, we should ONLY")
	o.L("// get either '{' or '}' here.")
	o.L("if tok == '}' { // End of object")
	o.L("break LOOP")
	o.L("} else if tok != '{' {")
	o.L("return fmt.Errorf(`expected '{', but got '%%c'`, tok)")
	o.L("}")
	o.L("case string: // Objects can only have string keys")
	o.L("switch tok {")
	// kty is special. Hardcode it.
	o.L("case KeyTypeKey:")
	o.L("val, err := json.ReadNextStringToken(dec)")
	o.L("if err != nil {")
	o.L("return fmt.Errorf(`error reading token: %%w`, err)")
	o.L("}")
	o.L("if val != %s.String() {", kt.KeyType)
	o.L("return fmt.Errorf(`invalid kty value for RSAPublicKey (%%s)`, val)")
	o.L("}")

	for _, f := range obj.Fields() {
		if f.Type() == "string" {
			o.L("case %sKey:", f.Name(true))
			o.L("if err := json.AssignNextStringToken(&h.%s, dec); err != nil {", f.Name(false))
			o.L("return fmt.Errorf(`failed to decode value for key %%s: %%w`, %sKey, err)", f.Name(true))
			o.L("}")
		} else if f.Type() == "jwa.KeyAlgorithm" {
			o.L("case %sKey:", f.Name(true))
			o.L("var s string")
			o.L("if err := dec.Decode(&s); err != nil {")
			o.L("return fmt.Errorf(`failed to decode value for key %%s: %%w`, %sKey, err)", f.Name(true))
			o.L("}")
			o.L("alg := jwa.KeyAlgorithmFrom(s)")
			o.L("h.%s = &alg", f.Name(false))
		} else if f.Type() == "[]byte" {
			name := f.Name(true)
			switch f.Name(false) {
			case "n", "e", "d", "p", "dp", "dq", "x", "y", "q", "qi", "octets":
				name = kt.Prefix + f.Name(true)
			}
			o.L("case %sKey:", name)
			o.L("if err := json.AssignNextBytesToken(&h.%s, dec); err != nil {", f.Name(false))
			o.L("return fmt.Errorf(`failed to decode value for key %%s: %%w`, %sKey, err)", name)
			o.L("}")
		} else {
			name := f.Name(true)
			if f.Name(false) == "crv" {
				name = kt.Prefix + f.Name(true)
			}
			o.L("case %sKey:", name)
			if IsPointer(f) {
				o.L("var decoded %s", PointerElem(f))
			} else {
				o.L("var decoded %s", f.Type())
			}
			o.L("if err := dec.Decode(&decoded); err != nil {")
			o.L("return fmt.Errorf(`failed to decode value for key %%s: %%w`, %sKey, err)", name)
			o.L("}")
			o.L("h.%s = &decoded", f.Name(false))
		}
	}
	o.L("default:")
	// This looks like bad code, but we're unrolling things for maximum
	// runtime efficiency
	o.L("if dc := h.dc; dc != nil {")
	o.L("if localReg := dc.Registry(); localReg != nil {")
	o.L("decoded, err := localReg.Decode(dec, tok)")
	o.L("if err == nil {")
	o.L("h.setNoLock(tok, decoded)")
	o.L("continue")
	o.L("}")
	o.L("}")
	o.L("}")

	o.L("decoded, err := registry.Decode(dec, tok)")
	o.L("if err == nil {")
	o.L("h.setNoLock(tok, decoded)")
	o.L("continue")
	o.L("}")
	o.L("return fmt.Errorf(`could not decode field %%s: %%w`, tok, err)")
	o.L("}")
	o.L("default:")
	o.L("return fmt.Errorf(`invalid token %%T`, tok)")
	o.L("}")
	o.L("}")

	for _, f := range obj.Fields() {
		if f.IsRequired() {
			o.L("if h.%s == nil {", f.Name(false))
			o.L("return fmt.Errorf(`required field %s is missing`)", f.JSON())
			o.L("}")
		}
	}

	o.L("return nil")
	o.L("}")

	o.LL("func (h %s) MarshalJSON() ([]byte, error) {", structName)
	o.L("data := make(map[string]interface{})")
	o.L("fields := make([]string, 0, %d)", len(obj.Fields()))
	o.L("for _, pair := range h.makePairs() {")
	o.L("fields = append(fields, pair.Key.(string))")
	o.L("data[pair.Key.(string)] = pair.Value")
	o.L("}")
	o.LL("sort.Strings(fields)")
	o.L("buf := pool.GetBytesBuffer()")
	o.L("defer pool.ReleaseBytesBuffer(buf)")
	o.L("buf.WriteByte('{')")
	o.L("enc := json.NewEncoder(buf)")
	o.L("for i, f := range fields {")
	o.L("if i > 0 {")
	o.L("buf.WriteRune(',')")
	o.L("}")
	o.L("buf.WriteRune('\"')")
	o.L("buf.WriteString(f)")
	o.L("buf.WriteString(`\":`)")
	o.L("v := data[f]")
	o.L("switch v := v.(type) {")
	o.L("case []byte:")
	o.L("buf.WriteRune('\"')")
	o.L("buf.WriteString(base64.EncodeToString(v))")
	o.L("buf.WriteRune('\"')")
	o.L("default:")
	o.L("if err := enc.Encode(v); err != nil {")
	o.L("return nil, fmt.Errorf(`failed to encode value for field %%s: %%w`, f, err)")
	o.L("}")
	o.L("buf.Truncate(buf.Len()-1)")
	o.L("}")
	o.L("}")
	o.L("buf.WriteByte('}')")
	o.L("ret := make([]byte, buf.Len())")
	o.L("copy(ret, buf.Bytes())")
	o.L("return ret, nil")
	o.L("}")

	o.LL("func (h *%s) Iterate(ctx context.Context) HeaderIterator {", structName)
	o.L("pairs := h.makePairs()")
	o.L("ch := make(chan *HeaderPair, len(pairs))")
	o.L("go func(ctx context.Context, ch chan *HeaderPair, pairs []*HeaderPair) {")
	o.L("defer close(ch)")
	o.L("for _, pair := range pairs {")
	o.L("select {")
	o.L("case <-ctx.Done():")
	o.L("return")
	o.L("case ch<-pair:")
	o.L("}")
	o.L("}")
	o.L("}(ctx, ch, pairs)")
	o.L("return mapiter.New(ch)")
	o.L("}")

	o.LL("func (h *%s) Walk(ctx context.Context, visitor HeaderVisitor) error {", structName)
	o.L("return iter.WalkMap(ctx, h, visitor)")
	o.L("}")

	o.LL("func (h *%s) AsMap(ctx context.Context) (map[string]interface{}, error) {", structName)
	o.L("return iter.AsMap(ctx, h)")
	o.L("}")

	return nil
}

func generateGenericHeaders(fields codegen.FieldList) error {
	var buf bytes.Buffer

	o := codegen.NewOutput(&buf)
	o.L("// Code generated by tools/cmd/genjwk/main.go. DO NOT EDIT.")
	o.LL("package jwk")

	o.LL("import (")
	pkgs := []string{
		"crypto/x509",
		"fmt",
		"github.com/lestrrat-go/jwx/v2/jwa",
	}
	for _, pkg := range pkgs {
		o.L("%s", strconv.Quote(pkg))
	}
	o.L(")")

	o.LL("const (")
	o.L("KeyTypeKey = \"kty\"")
	for _, f := range fields {
		o.L("%sKey = %s", f.Name(true), strconv.Quote(f.JSON()))
	}
	o.L(")") // end const

	o.LL("// Key defines the minimal interface for each of the")
	o.L("// key types. Their use and implementation differ significantly")
	o.L("// between each key types, so you should use type assertions")
	o.L("// to perform more specific tasks with each key")
	o.L("type Key interface {")
	o.L("// Get returns the value of a single field. The second boolean return value")
	o.L("// will be false if the field is not stored in the source")
	o.L("//\n// This method, which returns an `interface{}`, exists because")
	o.L("// these objects can contain extra _arbitrary_ fields that users can")
	o.L("// specify, and there is no way of knowing what type they could be")
	o.L("Get(string) (interface{}, bool)")
	o.LL("// Set sets the value of a single field. Note that certain fields,")
	o.L("// notably \"kty\", cannot be altered, but will not return an error")
	o.L("//\n// This method, which takes an `interface{}`, exists because")
	o.L("// these objects can contain extra _arbitrary_ fields that users can")
	o.L("// specify, and there is no way of knowing what type they could be")
	o.L("Set(string, interface{}) error")
	o.LL("// Remove removes the field associated with the specified key.")
	o.L("// There is no way to remove the `kty` (key type). You will ALWAYS be left with one field in a jwk.Key.")
	o.L("Remove(string) error")
	o.L("// Validate performs _minimal_ checks if the data stored in the key are valid.")
	o.L("// By minimal, we mean that it does not check if the key is valid for use in")
	o.L("// cryptographic operations. For example, it does not check if an RSA key's")
	o.L("// `e` field is a valid exponent, or if the `n` field is a valid modulus.")
	o.L("// Instead, it checks for things such as the _presence_ of some required fields,")
	o.L("// or if certain keys' values are of particular length.")
	o.L("//")
	o.L("// Note that depending on th underlying key type, use of this method requires")
	o.L("// that multiple fields in the key are properly populated. For example, an EC")
	o.L("// key's \"x\", \"y\" fields cannot be validated unless the \"crv\" field is populated first.")
	o.L("//")
	o.L("// Validate is never called by `UnmarshalJSON()` or `Set`. It must explicitly be")
	o.L("// called by the user")
	o.L("Validate() error")
	o.LL("// Raw creates the corresponding raw key. For example,")
	o.L("// EC types would create *ecdsa.PublicKey or *ecdsa.PrivateKey,")
	o.L("// and OctetSeq types create a []byte key.")
	o.L("//\n// If you do not know the exact type of a jwk.Key before attempting")
	o.L("// to obtain the raw key, you can simply pass a pointer to an")
	o.L("// empty interface as the first argument.")
	o.L("//\n// If you already know the exact type, it is recommended that you")
	o.L("// pass a pointer to the zero value of the actual key type (e.g. &rsa.PrivateKey)")
	o.L("// for efficiency.")
	o.L("Raw(interface{}) error")
	o.LL("// Thumbprint returns the JWK thumbprint using the indicated")
	o.L("// hashing algorithm, according to RFC 7638")
	o.L("Thumbprint(crypto.Hash) ([]byte, error)")
	o.LL("// Iterate returns an iterator that returns all keys and values.")
	o.L("// See github.com/lestrrat-go/iter for a description of the iterator.")
	o.L("Iterate(ctx context.Context) HeaderIterator")
	o.LL("// Walk is a utility tool that allows a visitor to iterate all keys and values")
	o.L("Walk(context.Context, HeaderVisitor) error")
	o.LL("// AsMap is a utility tool that returns a new map that contains the same fields as the source")
	o.L("AsMap(context.Context) (map[string]interface{}, error)")
	o.LL("// PrivateParams returns the non-standard elements in the source structure")
	o.L("// WARNING: DO NOT USE PrivateParams() IF YOU HAVE CONCURRENT CODE ACCESSING THEM.")
	o.L("// Use `AsMap()` to get a copy of the entire header, or use `Iterate()` instead")
	o.L("PrivateParams() map[string]interface{}")
	o.LL("// Clone creates a new instance of the same type")
	o.L("Clone() (Key, error)")
	o.LL("// PublicKey creates the corresponding PublicKey type for this object.")
	o.L("// All fields are copied onto the new public key, except for those that are not allowed.")
	o.L("//\n// If the key is already a public key, it returns a new copy minus the disallowed fields as above.")
	o.L("PublicKey() (Key, error)")
	o.LL("// KeyType returns the `kty` of a JWK")
	o.L("KeyType() jwa.KeyType")
	for _, f := range fields {
		o.L("// %s returns `%s` of a JWK", f.GetterMethod(true), f.JSON())
		if f.Name(false) == "algorithm" {
			o.LL("// Algorithm returns the value of the `alg` field")
			o.L("//")
			o.L("// This field may contain either `jwk.SignatureAlgorithm` or `jwk.KeyEncryptionAlgorithm`.")
			o.L("// This is why there exists a `jwa.KeyAlgorithm` type that encompasses both types.")
		}
		o.L("%s() ", f.GetterMethod(true))
		if v := f.String(`getter_return_value`); v != "" {
			o.R("%s", v)
		} else if IsPointer(f) && f.Bool(`noDeref`) {
			o.R("%s", f.Type())
		} else {
			o.R("%s", PointerElem(f))
		}
	}
	o.LL("makePairs() []*HeaderPair")
	o.L("}")

	if err := o.WriteFile("interface_gen.go", codegen.WithFormatCode(true)); err != nil {
		if cfe, ok := err.(codegen.CodeFormatError); ok {
			fmt.Fprint(os.Stderr, cfe.Source())
		}
		return fmt.Errorf(`failed to write to interface_gen.go: %w`, err)
	}
	return nil
}
