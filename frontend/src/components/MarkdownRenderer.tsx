import { useMemo } from 'react'
import { 
  Target, 
  Wrench, 
  Building2, 
  Code2, 
  Globe, 
  Shield, 
  Zap, 
  ClipboardList,
  AlertTriangle,
  CheckCircle2,
  Info
} from 'lucide-react'

interface MarkdownRendererProps {
  content: string
  className?: string
}

// Map emoji prefixes to icons and colors
const sectionStyles: Record<string, { icon: React.ElementType; color: string; bgColor: string; borderColor: string }> = {
  '🎯': { icon: Target, color: 'text-blue-400', bgColor: 'bg-blue-500/10', borderColor: 'border-blue-500/30' },
  '🛠️': { icon: Wrench, color: 'text-purple-400', bgColor: 'bg-purple-500/10', borderColor: 'border-purple-500/30' },
  '🏗️': { icon: Building2, color: 'text-cyan-400', bgColor: 'bg-cyan-500/10', borderColor: 'border-cyan-500/30' },
  '💻': { icon: Code2, color: 'text-green-400', bgColor: 'bg-green-500/10', borderColor: 'border-green-500/30' },
  '📂': { icon: Code2, color: 'text-green-400', bgColor: 'bg-green-500/10', borderColor: 'border-green-500/30' }, // AI sometimes uses this
  '🌐': { icon: Globe, color: 'text-amber-400', bgColor: 'bg-amber-500/10', borderColor: 'border-amber-500/30' },
  '🛡️': { icon: Shield, color: 'text-red-400', bgColor: 'bg-red-500/10', borderColor: 'border-red-500/30' },
  '⚡': { icon: Zap, color: 'text-yellow-400', bgColor: 'bg-yellow-500/10', borderColor: 'border-yellow-500/30' },
  '📋': { icon: ClipboardList, color: 'text-emerald-400', bgColor: 'bg-emerald-500/10', borderColor: 'border-emerald-500/30' },
}

// Parse and render markdown
function parseMarkdown(content: string): React.ReactNode[] {
  const lines = content.split('\n')
  const elements: React.ReactNode[] = []
  let currentList: string[] = []
  let inCodeBlock = false
  let codeContent: string[] = []
  let codeLanguage = ''

  const flushList = () => {
    if (currentList.length > 0) {
      elements.push(
        <ul key={`list-${elements.length}`} className="space-y-1.5 my-3 ml-4">
          {currentList.map((item, i) => (
            <li key={i} className="flex items-start gap-2 text-slate-300 text-sm">
              <span className="text-slate-500 mt-1.5">•</span>
              <span dangerouslySetInnerHTML={{ __html: formatInlineCode(item) }} />
            </li>
          ))}
        </ul>
      )
      currentList = []
    }
  }

  const flushCodeBlock = () => {
    if (codeContent.length > 0) {
      elements.push(
        <div key={`code-${elements.length}`} className="my-3 rounded-lg overflow-hidden border border-slate-700/50">
          {codeLanguage && (
            <div className="bg-slate-800 px-3 py-1 text-xs text-slate-500 border-b border-slate-700/50">
              {codeLanguage}
            </div>
          )}
          <pre className="bg-slate-900/80 p-3 overflow-x-auto">
            <code className="text-xs text-slate-300 font-mono">
              {codeContent.join('\n')}
            </code>
          </pre>
        </div>
      )
      codeContent = []
      codeLanguage = ''
    }
  }

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]

    // Code blocks
    if (line.startsWith('```')) {
      if (inCodeBlock) {
        flushCodeBlock()
        inCodeBlock = false
      } else {
        flushList()
        inCodeBlock = true
        codeLanguage = line.slice(3).trim()
      }
      continue
    }

    if (inCodeBlock) {
      codeContent.push(line)
      continue
    }

    // H2 headers with emoji - styled sections
    if (line.startsWith('## ')) {
      flushList()
      const headerText = line.slice(3)
      
      // Check for emoji prefix
      let matchedEmoji: string | null = null
      for (const emoji of Object.keys(sectionStyles)) {
        if (headerText.startsWith(emoji)) {
          matchedEmoji = emoji
          break
        }
      }

      if (matchedEmoji) {
        const style = sectionStyles[matchedEmoji]
        const Icon = style.icon
        const title = headerText.slice(matchedEmoji.length).trim()
        
        elements.push(
          <div key={`h2-${i}`} className={`mt-6 mb-3 p-3 rounded-lg ${style.bgColor} border ${style.borderColor}`}>
            <h2 className={`text-base font-bold flex items-center gap-2 ${style.color}`}>
              <Icon size={18} />
              {title}
            </h2>
          </div>
        )
      } else {
        elements.push(
          <h2 key={`h2-${i}`} className="text-base font-bold text-slate-200 mt-6 mb-3 pb-2 border-b border-slate-700/50">
            {headerText}
          </h2>
        )
      }
      continue
    }

    // H3 headers
    if (line.startsWith('### ')) {
      flushList()
      elements.push(
        <h3 key={`h3-${i}`} className="text-sm font-semibold text-slate-300 mt-4 mb-2">
          {line.slice(4)}
        </h3>
      )
      continue
    }

    // Numbered lists (recommendations)
    const numberedMatch = line.match(/^(\d+)\.\s+\*\*(.+?)\*\*:?\s*(.*)$/)
    if (numberedMatch) {
      flushList()
      const [, num, title, desc] = numberedMatch
      elements.push(
        <div key={`num-${i}`} className="flex items-start gap-3 my-2 p-2 bg-slate-800/30 rounded-lg">
          <span className="flex-shrink-0 w-6 h-6 rounded-full bg-emerald-500/20 text-emerald-400 text-xs font-bold flex items-center justify-center">
            {num}
          </span>
          <div>
            <span className="font-semibold text-slate-200 text-sm">{title}</span>
            {desc && <span className="text-slate-400 text-sm"> - {desc}</span>}
          </div>
        </div>
      )
      continue
    }

    // Regular numbered list
    const simpleNumbered = line.match(/^(\d+)\.\s+(.+)$/)
    if (simpleNumbered) {
      flushList()
      const [, num, text] = simpleNumbered
      elements.push(
        <div key={`snum-${i}`} className="flex items-start gap-3 my-1.5">
          <span className="flex-shrink-0 w-5 h-5 rounded-full bg-slate-700 text-slate-400 text-xs font-medium flex items-center justify-center">
            {num}
          </span>
          <span className="text-slate-300 text-sm" dangerouslySetInnerHTML={{ __html: formatInlineCode(text) }} />
        </div>
      )
      continue
    }

    // Bullet points
    if (line.match(/^[-*]\s+/)) {
      const content = line.replace(/^[-*]\s+/, '')
      currentList.push(content)
      continue
    }

    // Empty lines
    if (line.trim() === '') {
      flushList()
      continue
    }

    // Regular paragraphs
    flushList()
    elements.push(
      <p key={`p-${i}`} className="text-slate-300 text-sm my-2 leading-relaxed" 
         dangerouslySetInnerHTML={{ __html: formatInlineCode(line) }} />
    )
  }

  // Flush remaining
  flushList()
  flushCodeBlock()

  return elements
}

// Format inline code, bold, and line number references
function formatInlineCode(text: string): string {
  // Code blocks: `code`
  text = text.replace(/`([^`]+)`/g, '<code class="bg-slate-700/50 text-cyan-300 px-1.5 py-0.5 rounded text-xs font-mono">$1</code>')
  
  // Bold: **text**
  text = text.replace(/\*\*([^*]+)\*\*/g, '<strong class="text-slate-100 font-semibold">$1</strong>')
  
  // Line number references: (Lines X-Y) or (Line X)
  text = text.replace(/\(Lines?\s+(\d+)(?:-(\d+))?\)/gi, '<span class="bg-amber-500/20 text-amber-300 px-1.5 py-0.5 rounded text-xs font-mono">Lines $1$2</span>')
  
  // Italic: *text*
  text = text.replace(/(?<!\*)\*([^*]+)\*(?!\*)/g, '<em class="text-slate-400 italic">$1</em>')
  
  return text
}

export default function MarkdownRenderer({ content, className = '' }: MarkdownRendererProps) {
  const rendered = useMemo(() => parseMarkdown(content), [content])
  
  return (
    <div className={`markdown-content ${className}`}>
      {rendered}
    </div>
  )
}
