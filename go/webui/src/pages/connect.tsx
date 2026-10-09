import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Copy } from "lucide-react";
import clsx from "clsx";

import type { Route } from "@/lib/types";
import { useLocale } from "@/lib/i18n";
import { formatKeyPreview } from "@/lib/format";
import { NyroSearchSelect, NyroTextField } from "@/components/ui/nyro-fields";
import { ProviderIcon } from "@/components/ui/provider-icon";
import { SecretValue } from "@/components/ui/secret-value";
import { Notice } from "@/components/v2/notice";
import { PageHeader } from "@/components/v2/page-header";
import { PageLayout } from "@/components/v2/page-layout";
import { Status } from "@/components/v2/status";
import {
  API_KEY_ENV,
  CODE_LANGS,
  CODE_PROTOCOLS,
  codeTemplate,
  flattenKeys,
  languageLabel,
  protocolLabel,
  syntaxLanguage,
  tokenizeCode,
  type CodeLanguage,
  type CodeProtocol,
  type FlatKey,
  type SyntaxLanguage,
} from "@/features/connect/connect-code";
import { consumersApi } from "@/lib/api/consumers";
import { routesApi } from "@/lib/api/routes";
import { settingsApi } from "@/lib/api/settings";
import { localizedMessage } from "@/lib/messages";
import { showToast } from "@/lib/toast";

const PUBLIC_GATEWAY_URL_KEY = "gateway.public_url";
const DEFAULT_MAX_TOKENS = "1024";
// Fixed local base_url used in samples when gateway.public_url is not set.
const DEFAULT_LOCAL_BASE_URL = "http://127.0.0.1:19530";

/* §9.5 ①：代码块降级为纯 pre + token 着色（tokenizeCode），不再走
   react-syntax-highlighter。 */
function CodeTokens({ code, language }: { code: string; language: SyntaxLanguage }) {
  return (
    <>
      {tokenizeCode(code, language).map((token, index) =>
        token.kind === "plain" ? (
          token.text
        ) : (
          <span key={index} className={`tok-${token.kind}`}>{token.text}</span>
        ),
      )}
    </>
  );
}

export default function ConnectPage() {
  const { locale, t } = useLocale();
  const isZh = locale === "zh-CN";

  const [codeLang, setCodeLang] = useState<CodeLanguage>("python");
  const [selectedProtocol, setSelectedProtocol] = useState<CodeProtocol>("openai-compatible");
  const [selectedRouteId, setSelectedRouteId] = useState("");
  const [selectedKeyId, setSelectedKeyId] = useState("");
  const [stream, setStream] = useState(false);
  const [maxTokensInput, setMaxTokensInput] = useState(DEFAULT_MAX_TOKENS);

  const { data: routes = [] } = useQuery<Route[]>({
    queryKey: ["routes"],
    queryFn: () => routesApi.list(),
  });
  const { data: consumers = [] } = useQuery({
    queryKey: ["consumers"],
    queryFn: () => consumersApi.list(),
  });
  const { data: publicUrl } = useQuery<string | null>({
    queryKey: ["setting", PUBLIC_GATEWAY_URL_KEY],
    queryFn: () => settingsApi.get(PUBLIC_GATEWAY_URL_KEY),
  });

  const effectiveRouteID = routes.some((route) => route.id === selectedRouteId) ? selectedRouteId : "";
  const selectedRoute = useMemo(
    () => routes.find((r) => r.id === effectiveRouteID) ?? null,
    [routes, effectiveRouteID],
  );

  const flatKeys = useMemo(() => flattenKeys(consumers), [consumers]);
  // Plaintext mode is inferred from the presence of a recoverable token on any
  // key (only admin --plaintext-keys populates it). It gates both the key
  // dropdown and whether the sample can inline a real key.
  const plaintextMode = useMemo(() => flatKeys.some((k) => !!k.token), [flatKeys]);
  const availableKeys = useMemo(() => {
    if (!selectedRoute) return [];
    return flatKeys.filter((k) => k.grantsAll || k.routes.includes(selectedRoute.model));
  }, [flatKeys, selectedRoute]);

  const showKeyPicker = Boolean(selectedRoute?.enable_auth) && plaintextMode;

  const effectiveKeyID = showKeyPicker && availableKeys.some((key) => key.id === selectedKeyId) ? selectedKeyId : "";
  const selectedKey = useMemo(
    () => availableKeys.find((k) => k.id === effectiveKeyID) ?? null,
    [availableKeys, effectiveKeyID],
  );

  // base_url: a configured public URL wins; otherwise a fixed local address.
  const trimmedPublicUrl = (publicUrl ?? "").trim().replace(/\/+$/, "");
  const host = trimmedPublicUrl || DEFAULT_LOCAL_BASE_URL;

  // Inline a real key only when plaintext storage exposed one for the selected
  // key; otherwise the sample reads it from an environment variable.
  const realKey = showKeyPicker ? selectedKey?.token ?? "" : "";
  const useEnvVar = realKey === "";

  const parsedMaxTokens = (() => {
    const n = Number.parseInt(maxTokensInput, 10);
    return Number.isFinite(n) && n > 0 ? n : undefined;
  })();

  const generatedCode = codeTemplate({
    protocol: selectedProtocol,
    model: selectedRoute?.model ?? "gpt-4o",
    host,
    language: codeLang,
    stream,
    maxTokens: parsedMaxTokens,
    apiKeyLiteral: realKey,
    useEnvVar,
  });

  const enabledRoutes = useMemo(() => routes.filter((route) => route.enabled), [routes]);
  const selectedProtocolOption = CODE_PROTOCOLS.find((option) => option.id === selectedProtocol) ?? CODE_PROTOCOLS[0];

  // §9.5 ③：复制反馈统一走 Toast（端点 / 密钥 / 代码）。
  async function copyText(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      showToast(localizedMessage(isZh, "v2.api-keys.copied"));
    } catch {
      // Clipboard may be unavailable (insecure context); ignore silently.
    }
  }

  return (
    <PageLayout header={<PageHeader title={t("page.connect.title")} description={t("page.connect.subtitle")} />}>
      {/* §9.5 ②：网关端点 + 令牌展示卡（descriptions + secret-control）。 */}
      <section className="card">
        <div className="card-body">
          <dl className="descriptions">
            <dt>{localizedMessage(isZh, "v2.connect.gatewayEndpoint")}</dt>
            <dd>
              <span className="copy-value">
                <code>{host}</code>
                <button
                  type="button"
                  aria-label={localizedMessage(isZh, "common.copy")}
                  title={localizedMessage(isZh, "common.copy")}
                  onClick={() => void copyText(host)}
                >
                  <Copy aria-hidden="true" />
                </button>
              </span>
            </dd>
            <dt>{localizedMessage(isZh, "v2.connect.apiKey")}</dt>
            <dd>
              {realKey ? (
                <SecretValue value={realKey} ariaLabel={localizedMessage(isZh, "v2.connect.apiKey")} />
              ) : selectedRoute && !selectedRoute.enable_auth ? (
                localizedMessage(isZh, "v2.connect.notRequired")
              ) : (
                <>
                  <code>{API_KEY_ENV}</code>
                  {localizedMessage(isZh, "v2.connect.samplesReadTheKeyFromAnEnvironmentVariable")}
                </>
              )}
            </dd>
          </dl>
        </div>
      </section>

      <div className="connect-workspace">
        <section className="card">
          <header className="card-header">
            <div className="card-header-copy">
              <h2 className="card-title">{localizedMessage(isZh, "v2.connect.requestConfiguration")}</h2>
              <p className="card-subtitle">{localizedMessage(isZh, "v2.connect.changesUpdateTheCodeSampleImmediately")}</p>
            </div>
          </header>
          <div className="card-body connect-form">
            {/* §9.5 目标：协议切换 tabs。 */}
            <div className="field">
              <span className="field-label">{localizedMessage(isZh, "v2.connect.ingressProtocol")}</span>
              <div
                className="tabs tabs-inline"
                role="tablist"
                aria-label={localizedMessage(isZh, "v2.connect.ingressProtocol")}
                onKeyDown={(event) => {
                  const ids = CODE_PROTOCOLS.map((option) => option.id);
                  const current = Math.max(0, ids.indexOf(selectedProtocol));
                  if (event.key === "ArrowRight" || event.key === "ArrowDown") {
                    event.preventDefault();
                    setSelectedProtocol(ids[(current + 1) % ids.length]);
                  } else if (event.key === "ArrowLeft" || event.key === "ArrowUp") {
                    event.preventDefault();
                    setSelectedProtocol(ids[(current - 1 + ids.length) % ids.length]);
                  }
                }}
              >
                {CODE_PROTOCOLS.map((option) => {
                  const active = option.id === selectedProtocol;
                  return (
                    <button
                      key={option.id}
                      type="button"
                      role="tab"
                      aria-selected={active}
                      className={clsx("tab", active && "active")}
                      onClick={() => setSelectedProtocol(option.id)}
                    >
                      <ProviderIcon iconKey={option.iconKey} name={option.name} protocol={option.id} size={16} />
                      <span>{option.name}</span>
                    </button>
                  );
                })}
              </div>
              <span className="field-hint"><code>{selectedProtocolOption.apiPath}</code></span>
            </div>

            <NyroSearchSelect<Route>
              label={localizedMessage(isZh, "v2.connect.model")}
              options={enabledRoutes}
              value={selectedRoute}
              onChange={(route) => { setSelectedRouteId(route?.id ?? ""); setSelectedKeyId(""); }}
              getOptionLabel={(route) => route.model}
              isOptionEqualToValue={(a, b) => a.id === b.id}
              placeholder={routes.length ? (localizedMessage(isZh, "v2.connect.selectModel")) : (localizedMessage(isZh, "v2.connect.createAModelFirst"))}
              searchPlaceholder={localizedMessage(isZh, "common.search")}
              noOptionsText={localizedMessage(isZh, "v2.connect.noModelsAvailable")}
              hint={localizedMessage(isZh, "v2.connect.onlyEnabledModelRoutesAreShown")}
            />

            {showKeyPicker ? (
              <NyroSearchSelect<FlatKey>
                label={localizedMessage(isZh, "v2.connect.apiKey")}
                options={availableKeys}
                value={selectedKey}
                onChange={(key) => setSelectedKeyId(key?.id ?? "")}
                getOptionLabel={(key) => `${key.name} · ${formatKeyPreview(key.keyPreview)}`}
                isOptionEqualToValue={(a, b) => a.id === b.id}
                placeholder={localizedMessage(isZh, "v2.connect.selectApiKey")}
                searchPlaceholder={localizedMessage(isZh, "common.search")}
                noOptionsText={localizedMessage(isZh, "v2.connect.noApiKeysAvailable")}
                hint={localizedMessage(isZh, "v2.connect.theKeyMustBeAllowedToAccessThe")}
              />
            ) : (
              <Notice title={localizedMessage(isZh, "v2.connect.apiKey")}>
                {localizedMessage(isZh, "connect.keyFromEnvironment", { name: API_KEY_ENV })}
              </Notice>
            )}

            <div className="form-grid">
              <NyroTextField
                label={localizedMessage(isZh, "v2.api-keys.maxOutputTokens")}
                type="number"
                min={1}
                value={maxTokensInput}
                onChange={(event) => setMaxTokensInput(event.target.value)}
              />
              <div className="field">
                <div className="switch-row">
                  <div className="switch-copy">
                    <strong>{localizedMessage(isZh, "v2.connect.streaming")}</strong>
                    <span>{stream ? (localizedMessage(isZh, "v2.connect.enabled")) : (localizedMessage(isZh, "v2.settings.disabled"))}</span>
                  </div>
                  <button
                    type="button"
                    className={clsx("switch-control", stream && "on")}
                    aria-pressed={stream}
                    aria-label={localizedMessage(isZh, "v2.connect.streaming")}
                    onClick={() => setStream((current) => !current)}
                  />
                </div>
              </div>
            </div>
          </div>
        </section>

        {/* §9.5 ①：code-output / code-output-bar / code-copy 三件套。 */}
        <section className="code-output connect-code">
          <div className="code-output-bar">
            <div
              className="discover-switch"
              role="tablist"
              aria-label={localizedMessage(isZh, "v2.connect.codeLanguage")}
              onKeyDown={(event) => {
                const current = Math.max(0, CODE_LANGS.indexOf(codeLang));
                if (event.key === "ArrowRight" || event.key === "ArrowDown") {
                  event.preventDefault();
                  setCodeLang(CODE_LANGS[(current + 1) % CODE_LANGS.length]);
                } else if (event.key === "ArrowLeft" || event.key === "ArrowUp") {
                  event.preventDefault();
                  setCodeLang(CODE_LANGS[(current - 1 + CODE_LANGS.length) % CODE_LANGS.length]);
                }
              }}
            >
              {CODE_LANGS.map((language) => (
                <button
                  key={language}
                  type="button"
                  role="tab"
                  aria-selected={codeLang === language}
                  className={clsx(codeLang === language && "selected")}
                  onClick={() => setCodeLang(language)}
                >
                  {languageLabel(language)}
                </button>
              ))}
            </div>
            <span className="code-output-meta">
              {selectedRoute ? `${protocolLabel(selectedProtocol)} · ${selectedRoute.model}` : (localizedMessage(isZh, "v2.connect.waitingForAModel"))}
            </span>
            <button type="button" className="button button-sm" onClick={() => void copyText(generatedCode)}>
              <Copy aria-hidden="true" />{localizedMessage(isZh, "v2.connect.copyCode")}
            </button>
          </div>
          <pre className={clsx("code-output-body", !selectedRoute && "is-empty")}>
            {selectedRoute
              ? <CodeTokens code={generatedCode} language={syntaxLanguage(codeLang)} />
              : localizedMessage(isZh, "v2.connect.selectAModelToGenerateCodeHere")}
          </pre>
        </section>
      </div>

      <section className="card">
        <header className="card-header">
          <div className="card-header-copy">
            <h2 className="card-title">{localizedMessage(isZh, "v2.connect.integrationChecklist")}</h2>
            <p className="card-subtitle">{localizedMessage(isZh, "v2.connect.confirmTheseItemsBeforeRunningTheSample")}</p>
          </div>
        </header>
        <div className="card-body">
          <dl className="descriptions">
            <dt>{localizedMessage(isZh, "v2.connect.gatewayEndpoint")}</dt>
            <dd><Status tone="success">{localizedMessage(isZh, "v2.connect.configured")}</Status></dd>
            <dt>{localizedMessage(isZh, "v2.connect.modelRoute")}</dt>
            <dd>
              <Status tone={selectedRoute ? "success" : "warning"}>
                {selectedRoute?.model ?? (localizedMessage(isZh, "v2.connect.notSelected"))}
              </Status>
            </dd>
            <dt>{localizedMessage(isZh, "v2.connect.consumerKey")}</dt>
            <dd>
              <Status tone={!selectedRoute?.enable_auth || !showKeyPicker || selectedKey ? "success" : "warning"}>
                {!selectedRoute?.enable_auth
                  ? (localizedMessage(isZh, "v2.connect.notRequired"))
                  : useEnvVar
                    ? API_KEY_ENV
                    : (selectedKey?.name ?? (localizedMessage(isZh, "v2.connect.notSelected")))}
              </Status>
            </dd>
            <dt>{localizedMessage(isZh, "v2.connect.protocolTranslation")}</dt>
            <dd><Status tone="info">{protocolLabel(selectedProtocol)}</Status></dd>
          </dl>
        </div>
      </section>
    </PageLayout>
  );
}
