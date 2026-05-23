import { useMemo } from 'react'
import CodeMirror from '@uiw/react-codemirror'
import { python } from '@codemirror/lang-python'
import { autocompletion, type CompletionContext } from '@codemirror/autocomplete'

// CodeEditor is a Starlark (python-highlighted) editor whose autocomplete is
// driven by the live stdlib catalog from /api/capabilities.
export function CodeEditor({
  value, onChange, names, height = '340px',
}: {
  value: string
  onChange: (v: string) => void
  names: string[]
  height?: string
}) {
  const ext = useMemo(() => {
    const complete = autocompletion({
      override: [(ctx: CompletionContext) => {
        const word = ctx.matchBefore(/\w*/)
        if (!word || (word.from === word.to && !ctx.explicit)) return null
        return { from: word.from, options: names.map((n) => ({ label: n, type: 'function' })) }
      }],
    })
    return [python(), complete]
  }, [names])

  return <CodeMirror value={value} height={height} theme="dark" extensions={ext} onChange={onChange} />
}
