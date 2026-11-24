import { useState, type ReactNode } from "react"

import { Button } from "@/components/ui/button"
import { Kbd } from "@/components/ui/kbd"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Textarea } from "@/components/ui/textarea"

/** Compose 是锚定在触发按钮上的说明输入框, ⌘↩ 提交, 提交成功后收起. */
export function Compose({
  trigger,
  quote,
  placeholder,
  action,
  optional = false,
  pending,
  onSubmit,
}: {
  trigger: ReactNode
  quote?: string
  placeholder: string
  action: string
  optional?: boolean
  pending: boolean
  // done 在写入成功后调用, 收起并清空输入.
  onSubmit: (text: string, done: () => void) => void
}) {
  const [open, setOpen] = useState(false)
  const [text, setText] = useState("")
  const ready = !pending && (optional || text.trim() !== "")

  const submit = () => {
    if (!ready) return
    onSubmit(text.trim(), () => {
      setOpen(false)
      setText("")
    })
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>{trigger}</PopoverTrigger>
      <PopoverContent align="end" className="w-80 gap-2">
        {quote && <p className="line-clamp-3 text-muted-foreground">{quote}</p>}
        <Textarea
          autoFocus
          value={text}
          placeholder={placeholder}
          className="min-h-20 text-xs"
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) submit()
          }}
        />
        <div className="flex items-center justify-end gap-2">
          <Kbd>⌘↩</Kbd>
          <Button size="sm" disabled={!ready} onClick={submit}>
            {action}
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  )
}
