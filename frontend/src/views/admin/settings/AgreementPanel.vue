<script setup lang="ts">
import { useSettingsState } from "@/composables/useSettingsState";
import type { LoginAgreementDocument } from "@/types";
import Icon from "@/components/icons/Icon.vue";
import Toggle from "@/components/common/Toggle.vue";

const { form, localText } = useSettingsState();

function defaultLoginAgreementDocuments(): LoginAgreementDocument[] {
  return [
    {
      id: "terms",
      title: localText("服务条款", "Terms of Service"),
      content_md: "",
    },
    {
      id: "usage-policy",
      title: localText("使用政策", "Usage Policy"),
      content_md: "",
    },
    {
      id: "supported-regions",
      title: localText("支持的国家和地区", "Supported Countries and Regions"),
      content_md: "",
    },
    {
      id: "service-specific-terms",
      title: localText("服务特定条款", "Service-Specific Terms"),
      content_md: "",
    },
  ];
}

function normalizeLoginAgreementDocumentId(raw: string): string {
  return raw
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9_-]+/g, "-")
    .replace(/[-_]{2,}/g, "-")
    .replace(/^[-_]+|[-_]+$/g, "");
}

function loginAgreementRoutePath(
  doc: LoginAgreementDocument,
  index: number,
): string {
  const id =
    normalizeLoginAgreementDocumentId(doc.id || doc.title) || `doc-${index + 1}`;
  return `/legal/${id}`;
}

function addLoginAgreementDocument() {
  form.login_agreement_documents.push({
    id: `custom-${Date.now().toString(36)}`,
    title: "",
    content_md: "",
  });
}

function removeLoginAgreementDocument(index: number) {
  form.login_agreement_documents.splice(index, 1);
}

// Expose for parent (save-validation) if needed
defineExpose({
  defaultLoginAgreementDocuments,
  normalizeLoginAgreementDocumentId,
});
</script>

<template>
  <div class="space-y-6">
    <div class="card">
      <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
        <div class="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
          <div>
            <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
              {{ localText("登录条款确认", "Login agreement") }}
            </h2>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
              {{
                localText(
                  "控制登录页是否要求用户先阅读并同意服务条款、隐私政策或其他 Markdown 文档。",
                  "Control whether the login page requires users to accept Markdown policy documents first.",
                )
              }}
            </p>
          </div>
          <div class="flex items-center gap-3">
            <span class="text-sm text-gray-600 dark:text-gray-300">
              {{ form.login_agreement_enabled ? localText("已启用", "Enabled") : localText("未启用", "Disabled") }}
            </span>
            <Toggle v-model="form.login_agreement_enabled" />
          </div>
        </div>
      </div>

      <div class="space-y-6 p-6">
        <div class="grid grid-cols-1 gap-5 lg:grid-cols-[minmax(0,1fr)_220px]">
          <div>
            <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ localText("展示形式", "Display mode") }}
            </label>
            <div class="grid grid-cols-2 gap-2 rounded-lg bg-gray-100 p-1 dark:bg-dark-700">
              <button
                type="button"
                class="inline-flex items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium transition"
                :class="
                  form.login_agreement_mode === 'modal'
                    ? 'bg-white text-primary-700 shadow-sm dark:bg-dark-800 dark:text-primary-300'
                    : 'text-gray-600 hover:text-gray-900 dark:text-dark-300 dark:hover:text-white'
                "
                @click="form.login_agreement_mode = 'modal'"
              >
                <Icon name="shield" size="sm" />
                {{ localText("弹窗", "Modal") }}
              </button>
              <button
                type="button"
                class="inline-flex items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium transition"
                :class="
                  form.login_agreement_mode === 'checkbox'
                    ? 'bg-white text-primary-700 shadow-sm dark:bg-dark-800 dark:text-primary-300'
                    : 'text-gray-600 hover:text-gray-900 dark:text-dark-300 dark:hover:text-white'
                "
                @click="form.login_agreement_mode = 'checkbox'"
              >
                <Icon name="checkCircle" size="sm" />
                {{ localText("复选框", "Checkbox") }}
              </button>
            </div>
            <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
              {{
                form.login_agreement_mode === "checkbox"
                  ? localText("复选框会显示在登录按钮下方，未勾选前所有登录入口禁用。", "The checkbox appears below the login button and gates all login actions.")
                  : localText("弹窗会在登录页打开，用户拒绝后所有登录入口保持禁用。", "The modal opens on the login page and gates all login actions until accepted.")
              }}
            </p>
          </div>

          <div>
            <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ localText("条款更新日期", "Updated date") }}
            </label>
            <input
              v-model="form.login_agreement_updated_at"
              type="date"
              class="input"
            />
            <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
              {{ localText("日期或文档内容变化后，用户需要重新同意。", "Changing the date or content requires fresh consent.") }}
            </p>
          </div>
        </div>

        <div>
          <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
            <div>
              <h3 class="text-sm font-medium text-gray-900 dark:text-white">
                {{ localText("协议文档", "Agreement documents") }}
              </h3>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                {{
                  localText(
                    "文档名称可自定义，内容按 Markdown 保存。可参考：服务条款、使用政策、支持的国家和地区、服务特定条款。",
                    "Document titles are customizable and content is saved as Markdown.",
                  )
                }}
              </p>
            </div>
            <button
              type="button"
              class="btn btn-primary btn-sm inline-flex items-center gap-1.5"
              @click="addLoginAgreementDocument"
            >
              <Icon name="plus" size="sm" />
              {{ localText("添加文档", "Add document") }}
            </button>
          </div>

          <div class="mt-4 space-y-3">
            <div
              v-for="(doc, index) in form.login_agreement_documents"
              :key="doc.id || index"
              class="rounded-lg border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-800/60"
            >
              <div class="mb-3 flex items-center justify-between gap-3">
                <div class="flex min-w-0 items-center gap-3">
                  <span class="flex h-9 w-9 flex-shrink-0 items-center justify-center rounded-md bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-dark-200">
                    <Icon
                      :name="
                        index === 1
                          ? 'shield'
                          : index === 2
                            ? 'globe'
                            : index === 3
                              ? 'cog'
                              : 'document'
                      "
                      size="sm"
                    />
                  </span>
                  <div class="min-w-0">
                    <p class="truncate text-sm font-semibold text-gray-900 dark:text-white">
                      {{ doc.title || localText("未命名文档", "Untitled document") }}
                    </p>
                    <p class="truncate text-xs text-gray-500 dark:text-gray-400">
                      {{ loginAgreementRoutePath(doc, index) }}
                    </p>
                  </div>
                </div>
                <button
                  type="button"
                  class="rounded-md p-2 text-red-400 transition hover:bg-red-50 hover:text-red-600 disabled:cursor-not-allowed disabled:opacity-40 dark:hover:bg-red-900/20"
                  :disabled="
                    form.login_agreement_enabled &&
                    form.login_agreement_documents.length <= 1
                  "
                  @click="removeLoginAgreementDocument(index)"
                >
                  <Icon name="trash" size="sm" />
                </button>
              </div>

              <div class="grid grid-cols-1 gap-3 lg:grid-cols-2">
                <div>
                  <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
                    {{ localText("文档名称", "Document title") }}
                  </label>
                  <input
                    v-model="doc.title"
                    type="text"
                    class="input text-sm"
                    :placeholder="localText('例如：服务条款', 'Example: Terms of Service')"
                  />
                </div>
                <div>
                  <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
                    {{ localText("路由标识", "Route slug") }}
                  </label>
                  <div class="flex overflow-hidden rounded-lg border border-gray-300 bg-white focus-within:border-primary-500 focus-within:ring-1 focus-within:ring-primary-500 dark:border-dark-600 dark:bg-dark-900">
                    <span class="inline-flex flex-shrink-0 items-center border-r border-gray-200 bg-gray-50 px-3 text-sm text-gray-500 dark:border-dark-700 dark:bg-dark-800 dark:text-dark-400">
                      /legal/
                    </span>
                    <input
                      v-model="doc.id"
                      type="text"
                      class="min-w-0 flex-1 border-0 bg-transparent px-3 py-2 text-sm text-gray-900 outline-none placeholder:text-gray-400 focus:ring-0 dark:text-white dark:placeholder:text-dark-500"
                      placeholder="usage-policy"
                    />
                  </div>
                </div>
              </div>
              <div class="mt-3">
                <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
                  {{ localText("Markdown 内容", "Markdown content") }}
                </label>
                <textarea
                  v-model="doc.content_md"
                  rows="8"
                  class="input font-mono text-sm"
                  :placeholder="localText('在这里填写正式 Markdown 内容。', 'Write the final Markdown content here.')"
                ></textarea>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
