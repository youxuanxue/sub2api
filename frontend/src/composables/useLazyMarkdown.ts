import { ref, watch, type Ref } from 'vue'

/**
 * Lazy markdown rendering.
 *
 * `marked` (~40KB) + `dompurify` (~23KB) are only needed when a component
 * actually renders user/announcement/legal markdown — which for the
 * always-mounted shell components (AnnouncementPopup + AdminComplianceDialog,
 * statically imported by App.vue) is a rare event most sessions never hit. Static
 * top-level imports pulled both libraries into the eager app entry chunk, so they
 * downloaded + parsed on first paint of every page including login. Importing them
 * dynamically here keeps them out of the eager graph (Rollup splits them into the
 * on-demand `vendor-markdown` chunk; see vite.config.ts) and defers the cost to
 * the moment markdown is first shown.
 */

type MarkdownModules = {
  parse: (src: string) => string
  sanitize: (html: string) => string
}

let modulesPromise: Promise<MarkdownModules> | null = null

function loadMarkdownModules(): Promise<MarkdownModules> {
  if (!modulesPromise) {
    modulesPromise = Promise.all([import('marked'), import('dompurify')]).then(
      ([{ marked }, { default: DOMPurify }]) => {
        marked.setOptions({ breaks: true, gfm: true })
        return {
          parse: (src: string) => marked.parse(src) as string,
          sanitize: (html: string) => DOMPurify.sanitize(html),
        }
      },
    )
  }
  return modulesPromise
}

/**
 * Turn a reactive markdown source into a reactive sanitized-HTML ref. The heavy
 * markdown engine is loaded only when non-empty content first appears; until it
 * resolves (and for empty/blank content) the returned ref is ''. Bind the ref to
 * `v-html`.
 *
 * Accepts a ref or a getter so callers can pass `() => store.popup?.content`.
 */
export function useLazyMarkdown(
  source: Ref<string | null | undefined> | (() => string | null | undefined),
): Ref<string> {
  const html = ref('')
  let seq = 0
  watch(
    source,
    async (value) => {
      const mySeq = ++seq
      const content = (value ?? '').toString().trim()
      if (!content) {
        html.value = ''
        return
      }
      const mods = await loadMarkdownModules()
      // Drop the result if a newer source value superseded this render while the
      // (first-time) module import was in flight.
      if (mySeq !== seq) return
      html.value = mods.sanitize(mods.parse(content))
    },
    { immediate: true },
  )
  return html
}
