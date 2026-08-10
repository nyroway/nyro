import nyroLogoUrl from "@/assets/logos/NYRO-logo.png";

export function Brand() {
  return (
    <span className="v2-brand-lockup">
      <img src={nyroLogoUrl} alt="Nyro" width={32} height={32} />
      <span className="v2-brand-name">NYRO</span>
      <small>CONSOLE</small>
    </span>
  );
}
