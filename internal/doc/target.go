package doc

// Target 是 doc、doc#sec 或 doc#container/item 形式的定位路径.
type Target struct {
	Doc     string
	Section string
	// Item 只在容器局部命名空间的路径中出现, 此时 Section 是容器 ID.
	Item string
}

// ParseTarget 解析定位路径, 允许带前导 @.
func ParseTarget(s string) (Target, error) {
	panic("TODO")
}

// String 返回规范形式, 不带前导 @.
func (t Target) String() string {
	panic("TODO")
}
