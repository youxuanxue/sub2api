import { onMounted, onUnmounted, nextTick } from 'vue'
import { driver, type Driver, type DriveStep } from 'driver.js'
import 'driver.js/dist/driver.css'
import { useAuthStore as useUserStore } from '@/stores/auth'
import { useOnboardingStore } from '@/stores/onboarding'
import { useI18n } from 'vue-i18n'
import { getAdminSteps, getUserSteps } from '@/components/Guide/steps'
import { userAPI } from '@/api/user'

export interface OnboardingOptions {
  storageKey?: string
  autoStart?: boolean
}

export function useOnboardingTour(options: OnboardingOptions) {
  const { t } = useI18n()
  const userStore = useUserStore()
  const onboardingStore = useOnboardingStore()
  const storageVersion = 'v4_interactive' // Bump version for new tour type

  // Timing constants for better maintainability
  const TIMING = {
    INTERACTIVE_WAIT_MS: 800,        // Default wait time for interactive steps
    ELEMENT_TIMEOUT_MS: 8000,        // Timeout for element detection
    AUTO_START_DELAY_MS: 1000        // Delay before auto-starting tour
  } as const

  // Helper: Check if a step is interactive (only close button shown)
  const isInteractiveStep = (step: DriveStep): boolean => {
    return step.popover?.showButtons?.length === 1 &&
           step.popover.showButtons[0] === 'close'
  }

  // Helper: Clean up click listener
  const cleanupClickListener = () => {
    if (!currentClickListener) return
    const { element: el, handler, keyHandler, originalTabIndex, eventTypes } = currentClickListener
    if (eventTypes) {
      eventTypes.forEach(type => el.removeEventListener(type, handler))
    }
    if (keyHandler) el.removeEventListener('keydown', keyHandler)
    if (originalTabIndex !== undefined) {
      if (originalTabIndex === null) el.removeAttribute('tabindex')
      else el.setAttribute('tabindex', originalTabIndex)
    }
    currentClickListener = null
  }

  // 使用 store 管理的全局 driver 实例
  let driverInstance: Driver | null = onboardingStore.getDriverInstance()
  let currentClickListener: {
    element: HTMLElement
    handler: () => void
    keyHandler?: (e: KeyboardEvent) => void
    originalTabIndex?: string | null
    eventTypes?: string[] // Track which event types were added
  } | null = null
  let autoStartTimer: ReturnType<typeof setTimeout> | null = null
  let globalKeyboardHandler: ((e: KeyboardEvent) => void) | null = null

  const getStorageKey = () => {
    const baseKey = options.storageKey ?? 'onboarding_tour'
    const userId = userStore.user?.id ?? 'guest'
    const role = userStore.user?.role ?? 'user'
    return `${baseKey}_${userId}_${role}_${storageVersion}`
  }

  // US-031: server-side memory of "已看过" trumps localStorage. We still keep
  // the localStorage flag as a fast-path cache (avoids re-launching the tour
  // mid-session before the next /user/profile refresh lands), but the source
  // of truth for "should we auto-launch" is the server-side
  // user.onboarding_tour_seen_at — see auto-start gate in onMounted below.
  const hasSeenLocal = () => {
    return localStorage.getItem(getStorageKey()) === 'true'
  }

  const hasSeenServer = () => {
    return !!userStore.user?.onboarding_tour_seen_at
  }

  // hasSeen returns true when the user has either (a) been recorded
  // server-side as having seen the tour, or (b) flagged it locally in this
  // session. The server flag is the durable one; the local flag is only
  // useful for the current page lifecycle.
  const hasSeen = () => hasSeenServer() || hasSeenLocal()

  const markAsSeen = () => {
    localStorage.setItem(getStorageKey(), 'true')
    // Best-effort server-side persistence (US-031 AC-005 + AC-006). We do
    // NOT await this — the UX of closing the tour must not block on a
    // network round-trip, and a failure simply means the next dashboard
    // load will re-launch the tour (the user retries naturally).
    if (userStore.user) {
      userAPI.markOnboardingTourSeen().catch((err) => {
        console.error('[Onboarding] mark_seen_failed', err)
      })
    }
  }

  // replayTour clears the LOCAL flag only. The server-side timestamp is
  // never cleared — it is a permanent record of "they have seen this once",
  // and clearing it would let "已看过" silently regress on the next reload.
  const clearSeen = () => {
    localStorage.removeItem(getStorageKey())
  }

  /**
   * 检查元素是否存在，如果不存在则重试
   */
  const ensureElement = async (selector: string, timeout = 5000): Promise<boolean> => {
    const startTime = Date.now()
    while (Date.now() - startTime < timeout) {
      const element = document.querySelector(selector)
      if (element && element.getBoundingClientRect().height > 0) {
        return true
      }
      await new Promise((resolve) => setTimeout(resolve, 150))
    }
    return false
  }

  const startTour = async (startIndex = 0) => {
    // 动态获取当前用户角色和步骤
    const isAdmin = userStore.user?.role === 'admin'
    const isSimpleMode = userStore.isSimpleMode
    const steps = isAdmin ? getAdminSteps(t, isSimpleMode) : getUserSteps(t)

    // 确保 DOM 就绪
    await nextTick()

    // 如果指定了起始步骤，确保元素可见
    const currentStep = steps[startIndex]
    if (currentStep?.element && typeof currentStep.element === 'string') {
      await ensureElement(currentStep.element, TIMING.ELEMENT_TIMEOUT_MS)
    }

    if (driverInstance) {
      driverInstance.destroy()
    }

    // 创建新的 driver 实例并存储到 store
    driverInstance = driver({
      showProgress: true,
      steps,
      animate: true,
      allowClose: false, // 禁止点击遮罩关闭
      stagePadding: 4,
      popoverClass: 'theme-tour-popover',
      nextBtnText: t('common.next'),
      prevBtnText: t('common.back'),
      doneBtnText: t('common.confirm'),

      // 导航处理
      onNextClick: async (_el, _step, { config, state }) => {
        // 如果是最后一步，点击则是"完成"
        if (state.activeIndex === (config.steps?.length ?? 0) - 1) {
          markAsSeen()
          driverInstance?.destroy()
          onboardingStore.setDriverInstance(null)
        } else {
          // 注意：交互式步骤通常隐藏 Next 按钮，此处逻辑为防御性编程
          const currentIndex = state.activeIndex ?? 0
          const currentStep = steps[currentIndex]

          if (currentStep && isInteractiveStep(currentStep) && currentStep.element) {
            const targetElement = typeof currentStep.element === 'string'
              ? document.querySelector(currentStep.element) as HTMLElement
              : currentStep.element as HTMLElement

            if (targetElement) {
              const isClickable = !['INPUT', 'TEXTAREA', 'SELECT'].includes(targetElement.tagName)
              if (isClickable) {
                targetElement.click()
                return
              }
            }
          }
          driverInstance?.moveNext()
        }
      },
      onPrevClick: () => {
        driverInstance?.movePrevious()
      },
      onCloseClick: () => {
        markAsSeen()
        driverInstance?.destroy()
        onboardingStore.setDriverInstance(null)
      },

      // 渲染时重组 Footer 布局
      onPopoverRender: (popover, { config, state }) => {
        // Class name constants for easier maintenance
        const CLASS_REORGANIZED = 'reorganized'
        const CLASS_FOOTER_LEFT = 'footer-left'
        const CLASS_FOOTER_RIGHT = 'footer-right'
        const CLASS_DONE_BTN = 'driver-popover-done-btn'
        const CLASS_PROGRESS_TEXT = 'driver-popover-progress-text'
        const CLASS_NEXT_BTN = 'driver-popover-next-btn'
        const CLASS_PREV_BTN = 'driver-popover-prev-btn'

        try {
          const { title: titleEl, footer: footerEl, nextButton, previousButton } = popover

          // Defensive check: ensure popover elements exist
          if (!titleEl || !footerEl) {
            console.warn('Onboarding: Missing popover elements')
            return
          }

          // 1.5 交互式步骤提示
          const currentStep = steps[state.activeIndex ?? 0]

          if (currentStep && isInteractiveStep(currentStep) && popover.description) {
            const hintClass = 'driver-popover-description-hint'
            if (!popover.description.querySelector(`.${hintClass}`)) {
              const hint = document.createElement('div')
              hint.className = `${hintClass} mt-2 text-xs text-gray-500 flex items-center gap-1`

              const iconSpan = document.createElement('span')
              iconSpan.className = 'i-mdi-keyboard-return mr-1'

              const textNode = document.createTextNode(
                t('onboarding.interactiveHint', 'Press Enter or Click to continue'),
              )

              hint.appendChild(iconSpan)
              hint.appendChild(textNode)
              popover.description.appendChild(hint)
            }
          }

          // 2. 底部：DOM 重组
          if (!footerEl.classList.contains(CLASS_REORGANIZED)) {
            footerEl.classList.add(CLASS_REORGANIZED)

            const progressEl = footerEl.querySelector(`.${CLASS_PROGRESS_TEXT}`)
            const nextBtnEl = nextButton || footerEl.querySelector(`.${CLASS_NEXT_BTN}`)
            const prevBtnEl = previousButton || footerEl.querySelector(`.${CLASS_PREV_BTN}`)

            const leftContainer = document.createElement('div')
            leftContainer.className = CLASS_FOOTER_LEFT

            const rightContainer = document.createElement('div')
            rightContainer.className = CLASS_FOOTER_RIGHT

            if (progressEl) leftContainer.appendChild(progressEl)

            const shortcutsEl = document.createElement('div')
            shortcutsEl.className = 'footer-shortcuts'

            const shortcut1 = document.createElement('span')
            shortcut1.className = 'shortcut-item'
            const kbd1 = document.createElement('kbd')
            kbd1.textContent = '←'
            const kbd2 = document.createElement('kbd')
            kbd2.textContent = '→'
            shortcut1.appendChild(kbd1)
            shortcut1.appendChild(kbd2)
            shortcut1.appendChild(
              document.createTextNode(` ${t('onboarding.navigation.flipPage')}`),
            )

            const shortcut2 = document.createElement('span')
            shortcut2.className = 'shortcut-item'
            const kbd3 = document.createElement('kbd')
            kbd3.textContent = 'ESC'
            shortcut2.appendChild(kbd3)
            shortcut2.appendChild(
              document.createTextNode(` ${t('onboarding.navigation.exit')}`),
            )

            shortcutsEl.appendChild(shortcut1)
            shortcutsEl.appendChild(shortcut2)
            leftContainer.appendChild(shortcutsEl)

            if (prevBtnEl) rightContainer.appendChild(prevBtnEl)
            if (nextBtnEl) rightContainer.appendChild(nextBtnEl)

            footerEl.innerHTML = ''
            footerEl.appendChild(leftContainer)
            footerEl.appendChild(rightContainer)
          }

          // 3. 状态更新
          const isLastStep = state.activeIndex === (config.steps?.length ?? 0) - 1
          const activeNextBtn = nextButton || footerEl.querySelector(`.${CLASS_NEXT_BTN}`)

          if (activeNextBtn) {
             if (isLastStep) {
               activeNextBtn.classList.add(CLASS_DONE_BTN)
             } else {
               activeNextBtn.classList.remove(CLASS_DONE_BTN)
             }
          }
        } catch (e) {
          console.error('Onboarding Tour Render Error:', e)
        }
      },

      // 步骤高亮时触发
      onHighlightStarted: async (element, step) => {
        // 清理之前的监听器
        cleanupClickListener()

        // 尝试等待元素
        if (!element && step.element && typeof step.element === 'string') {
           const exists = await ensureElement(step.element, 8000)
           if (!exists) {
             console.warn(`Tour element not found after 8s: ${step.element}`)
             return
           }
           element = document.querySelector(step.element) as HTMLElement
        }

        if (isInteractiveStep(step) && element) {
          const htmlElement = element as HTMLElement

          // Check if this is a submit button - if so, don't bind auto-advance listeners
          // Let business code (e.g., handleCreateGroup) manually call nextStep after success
          const isSubmitButton = htmlElement.getAttribute('type') === 'submit' ||
                                (htmlElement.tagName === 'BUTTON' && htmlElement.closest('form'))

          if (isSubmitButton) {
            return // Don't bind any click listeners for submit buttons
          }

          const originalTabIndex = htmlElement.getAttribute('tabindex')
          if (!htmlElement.isContentEditable && htmlElement.tabIndex === -1) {
             htmlElement.setAttribute('tabindex', '0')
          }

          // Enhanced Select component detection - check both children and self
          const isSelectComponent = htmlElement.querySelector('.select-trigger') !== null ||
                                    htmlElement.classList.contains('select-trigger')

          // Select dropdowns are teleported to <body>, so click events on options
          // won't bubble through this element. Skip auto-advance for Select components.
          // Users navigate using Next/Previous buttons after making their selection.
          if (isSelectComponent) {
            return
          }

          // Single-execution protection flag
          let hasExecuted = false

          // Capture the step index when binding the handler
          const boundStepIndex = driverInstance?.getActiveIndex() ?? 0

          const clickHandler = async () => {
            // Prevent duplicate execution
            if (hasExecuted) {
              return
            }
            hasExecuted = true

            // Wait before advancing to allow user to see the result of their action
            await new Promise(resolve => setTimeout(resolve, TIMING.INTERACTIVE_WAIT_MS))

            // Verify driver is still active and not destroyed
            if (!driverInstance || !driverInstance.isActive()) {
              return
            }

            // Check if we're still on the same step - abort if step changed during wait
            const currentIndex = driverInstance.getActiveIndex() ?? 0
            if (currentIndex !== boundStepIndex) {
              return
            }

            const nextStep = steps[currentIndex + 1]

            if (nextStep?.element && typeof nextStep.element === 'string') {
              const exists = await ensureElement(nextStep.element, TIMING.ELEMENT_TIMEOUT_MS)
              if (!exists) {
                console.warn(`Onboarding: Next step element not found: ${nextStep.element}`)
                return
              }
            }

            // Final check before moving
            if (driverInstance && driverInstance.isActive()) {
              driverInstance.moveNext()
            }
          }

          // For input fields, advance on input/change events instead of click
          const isInputField = ['INPUT', 'TEXTAREA', 'SELECT'].includes(htmlElement.tagName)

          if (isInputField) {
            const inputHandler = () => {
              // Remove listener after first input
              htmlElement.removeEventListener('input', inputHandler)
              htmlElement.removeEventListener('change', inputHandler)
              clickHandler()
            }

            htmlElement.addEventListener('input', inputHandler)
            htmlElement.addEventListener('change', inputHandler)

            currentClickListener = {
              element: htmlElement,
              handler: inputHandler,
              originalTabIndex,
              eventTypes: ['input', 'change']
            }
          } else {
            const keyHandler = (e: KeyboardEvent) => {
               if (['Enter', ' '].includes(e.key)) {
                  e.preventDefault()
                  clickHandler()
               }
            }

            htmlElement.addEventListener('click', clickHandler, { once: true })
            htmlElement.addEventListener('keydown', keyHandler)

            currentClickListener = {
              element: htmlElement,
              handler: clickHandler as () => void,
              keyHandler,
              originalTabIndex,
              eventTypes: ['click']
            }
          }
        }
      },

      onDestroyed: () => {
        cleanupClickListener()
        // 清理全局监听器 (由此处唯一管理)
        if (globalKeyboardHandler) {
          document.removeEventListener('keydown', globalKeyboardHandler, { capture: true })
          globalKeyboardHandler = null
        }
        onboardingStore.setDriverInstance(null)
      }
    })

    onboardingStore.setDriverInstance(driverInstance)

    // 添加全局键盘监听器
    globalKeyboardHandler = (e: KeyboardEvent) => {
      if (!driverInstance?.isActive()) return

      if (e.key === 'Escape') {
        e.preventDefault()
        e.stopPropagation()
        markAsSeen()
        driverInstance.destroy()
        onboardingStore.setDriverInstance(null)
        return
      }

      if (e.key === 'ArrowRight') {
        const target = e.target as HTMLElement
        // 允许在输入框中使用方向键
        if (['INPUT', 'TEXTAREA'].includes(target?.tagName)) {
           return
        }

        e.preventDefault()
        e.stopPropagation()

        // 对于交互式步骤，箭头键应该触发交互而非跳过
        const currentIndex = driverInstance!.getActiveIndex() ?? 0
        const currentStep = steps[currentIndex]

        if (currentStep && isInteractiveStep(currentStep) && currentStep.element) {
          const targetElement = typeof currentStep.element === 'string'
            ? document.querySelector(currentStep.element) as HTMLElement
            : currentStep.element as HTMLElement

          if (targetElement) {
            // 对于非输入类元素，提示用户需要点击或按Enter
            const isClickable = !['INPUT', 'TEXTAREA', 'SELECT'].includes(targetElement.tagName)
            if (isClickable) {
              // 不自动触发，只是停留提示
              return
            }
          }
        }

        // 非交互式步骤才允许箭头键翻页
        driverInstance!.moveNext()
      }
      else if (e.key === 'Enter') {
        const target = e.target as HTMLElement
        // 允许在输入框中使用回车
        if (['INPUT', 'TEXTAREA'].includes(target?.tagName)) {
           return
        }

        e.preventDefault()
        e.stopPropagation()

        // 回车键处理交互式步骤
        const currentIndex = driverInstance!.getActiveIndex() ?? 0
        const currentStep = steps[currentIndex]

        if (currentStep && isInteractiveStep(currentStep) && currentStep.element) {
          const targetElement = typeof currentStep.element === 'string'
            ? document.querySelector(currentStep.element) as HTMLElement
            : currentStep.element as HTMLElement

          if (targetElement) {
            const isClickable = !['INPUT', 'TEXTAREA', 'SELECT'].includes(targetElement.tagName)
            if (isClickable) {
              targetElement.click()
              return
            }
          }
        }
        driverInstance!.moveNext()
      }
      else if (e.key === 'ArrowLeft') {
        const target = e.target as HTMLElement
        // 允许在输入框中使用方向键
        if (['INPUT', 'TEXTAREA', 'SELECT'].includes(target?.tagName) || target?.isContentEditable) {
           return
        }

        e.preventDefault()
        e.stopPropagation()
        driverInstance.movePrevious()
      }
    }

    document.addEventListener('keydown', globalKeyboardHandler, { capture: true })
    driverInstance.drive(startIndex)
  }

  const nextStep = async (delay = 300) => {
    if (!driverInstance?.isActive()) return
    if (delay > 0) {
      await new Promise(resolve => setTimeout(resolve, delay))
    }
    driverInstance.moveNext()
  }

  const isCurrentStep = (elementSelector: string): boolean => {
    if (!driverInstance?.isActive()) return false
    const activeElement = driverInstance.getActiveElement()
    return activeElement?.matches(elementSelector) ?? false
  }

  const replayTour = () => {
    clearSeen()
    void startTour()
  }

  onMounted(async () => {
    onboardingStore.setControlMethods({
      nextStep,
      isCurrentStep
    })

    if (onboardingStore.isDriverActive()) {
      driverInstance = onboardingStore.getDriverInstance()
      return
    }

    // 没有用户上下文时不启动（防御性：composable 通常挂在认证页面，
    // 但 ShellLayout 的初始化时序可能在 user fetch 完成前触发 onMounted）。
    if (!userStore.user) {
      return
    }

    // 简易模式下禁用新手引导
    if (userStore.isSimpleMode) {
      return
    }

    // US-031 PR 2 P1-A: tour now auto-launches for BOTH admin and regular
    // users. The previous `if (!isAdmin) return` gate meant普通 users on
    // a fresh signup never saw any orientation — they landed on a dashboard
    // with no obvious next step (L 站 t/topic/1413702 反馈).
    //
    // The decision of "已看过 → 不再自动启动" now consults the server-side
    // `user.onboarding_tour_seen_at` field (durable across devices / cache
    // clears) plus the legacy localStorage flag as a same-session cache.
    if (!options.autoStart || hasSeen()) return
    autoStartTimer = setTimeout(() => {
      void startTour()
    }, TIMING.AUTO_START_DELAY_MS)
  })

  onUnmounted(() => {
    if (autoStartTimer) {
      clearTimeout(autoStartTimer)
      autoStartTimer = null
    }
    // 关键修复：不再此处清理 globalKeyboardHandler，交由 driver.onDestroyed 管理
    onboardingStore.clearControlMethods()
  })

  return {
    startTour,
    replayTour,
    nextStep,
    isCurrentStep,
    hasSeen,
    markAsSeen,
    clearSeen
  }
}
