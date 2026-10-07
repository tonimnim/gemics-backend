import playerLeft from '@/assets/login-player-left.webp'
import playerRight from '@/assets/login-player-right.webp'
import { Logo } from '@/assets/logo'

type AuthLayoutProps = {
  children: React.ReactNode
}

/** Dotted halftone discs, echoing the dashboard's overview banner. */
function Dots({ className }: { className: string }) {
  return (
    <div
      className={`pointer-events-none absolute rounded-full bg-[radial-gradient(circle,rgba(91,91,214,0.28)_1.6px,transparent_1.6px)] [background-size:10px_10px] ${className}`}
    />
  )
}

/** The image side: the player rising out of an indigo spotlight. */
function Hero({ compact = false }: { compact?: boolean }) {
  return (
    <div className='relative h-full overflow-hidden rounded-[2rem] bg-[linear-gradient(150deg,#eef0ff_0%,#e3e7ff_45%,#d6e2ff_100%)] dark:bg-[linear-gradient(150deg,#1d1f45_0%,#1a1c3d_50%,#16203f_100%)]'>
      <Dots className='-top-24 -left-20 size-80 opacity-60' />
      <Dots className='right-[18%] -bottom-32 size-96 opacity-40' />
      {/* Two spotlights, one behind each player, meeting in the middle. */}
      <div className='absolute -bottom-[22%] -left-[16%] aspect-square w-[62%] rounded-full bg-[radial-gradient(circle_at_60%_30%,#8b8cf0,#5b5bd6_55%,#4141b8)] opacity-95' />
      <div className='absolute -right-[16%] -bottom-[22%] aspect-square w-[62%] rounded-full bg-[radial-gradient(circle_at_40%_30%,#8b8cf0,#5b5bd6_55%,#4141b8)] opacity-95' />
      <div className='absolute bottom-0 left-1/2 aspect-square w-[46%] -translate-x-1/2 translate-y-1/2 rounded-full bg-[radial-gradient(circle,rgba(255,255,255,0.55),transparent_70%)]' />

      {/* Same height, same baseline, facing each other across the panel. */}
      <img
        src={playerLeft}
        alt=''
        className={
          compact
            ? 'absolute bottom-0 -left-[5%] w-[52%] max-w-none'
            : 'absolute bottom-0 -left-[6%] w-[53%] max-w-none drop-shadow-[0_30px_40px_rgba(27,29,58,0.28)]'
        }
      />
      <img
        src={playerRight}
        alt=''
        className={
          compact
            ? 'absolute -right-[5%] bottom-0 w-[52%] max-w-none'
            : 'absolute -right-[6%] bottom-0 w-[53%] max-w-none drop-shadow-[0_30px_40px_rgba(27,29,58,0.28)]'
        }
      />

      {compact ? (
        <Logo
          aria-label='Logo'
          className='absolute top-5 left-5 z-10 size-12 text-primary'
        />
      ) : (
        <div className='relative z-10 flex items-center gap-4 p-10 xl:gap-5 xl:p-12'>
          <Logo
            aria-label='Logo'
            className='size-14 shrink-0 text-primary xl:size-16'
          />
          <h2 className='text-2xl leading-tight font-bold tracking-tight text-[#1b1d3a] xl:text-3xl dark:text-white'>
            Run every match.
            <br />
            <span className='bg-gradient-to-r from-[#5b5bd6] to-[#8b8cf0] bg-clip-text text-transparent'>
              Settle every score.
            </span>
          </h2>
        </div>
      )}
    </div>
  )
}

export function AuthLayout({ children }: AuthLayoutProps) {
  return (
    <div className='grid min-h-svh bg-background lg:grid-cols-[1.15fr_1fr]'>
      <div className='hidden p-3 lg:block'>
        <Hero />
      </div>
      <div className='flex flex-col'>
        <div className='h-56 p-3 lg:hidden'>
          <Hero compact />
        </div>
        <div className='flex flex-1 items-center justify-center px-6 py-10 sm:px-10'>
          <div className='w-full max-w-[24rem]'>{children}</div>
        </div>
      </div>
    </div>
  )
}
