import { useState } from 'react'
import { z } from 'zod'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { useNavigate } from '@tanstack/react-router'
import {
  ArrowRight,
  Eye,
  EyeOff,
  IdCard,
  KeyRound,
  Loader2,
} from 'lucide-react'
import { errorMessage } from '@/lib/api'
import { signIn } from '@/lib/auth'
import { cn } from '@/lib/utils'

const formSchema = z.object({
  konamiId: z.string().trim().min(1, 'Enter your Konami ID.'),
  password: z.string().min(1, 'Enter your password.'),
})

type FormValues = z.infer<typeof formSchema>

interface UserAuthFormProps extends React.HTMLAttributes<HTMLFormElement> {
  redirectTo?: string
}

const inputClass =
  'h-12 w-full rounded-xl border border-input bg-card ps-11 pe-4 text-sm outline-none transition placeholder:text-muted-foreground/70 focus:border-primary focus:ring-4 focus:ring-primary/15 aria-[invalid=true]:border-destructive aria-[invalid=true]:ring-destructive/15'

function Field({
  id,
  label,
  icon: Icon,
  error,
  children,
}: {
  id: string
  label: string
  icon: React.ElementType
  error?: string
  children: React.ReactNode
}) {
  return (
    <div className='grid gap-2'>
      <label htmlFor={id} className='text-sm font-medium'>
        {label}
      </label>
      <div className='relative'>
        <Icon className='pointer-events-none absolute start-4 top-1/2 size-4 -translate-y-1/2 text-muted-foreground' />
        {children}
      </div>
      {error && <p className='text-xs text-destructive'>{error}</p>}
    </div>
  )
}

/** Staff sign in with the same Konami ID and password as the player app. */
export function UserAuthForm({
  className,
  redirectTo,
  ...props
}: UserAuthFormProps) {
  const [isLoading, setIsLoading] = useState(false)
  const [error, setError] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const navigate = useNavigate()
  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<FormValues>({
    resolver: zodResolver(formSchema),
    defaultValues: { konamiId: '', password: '' },
  })

  async function onSubmit(data: FormValues) {
    setIsLoading(true)
    setError('')
    try {
      await signIn(data.konamiId, data.password)
      navigate({ to: redirectTo || '/', replace: true })
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setIsLoading(false)
    }
  }

  return (
    <form
      onSubmit={handleSubmit(onSubmit)}
      className={cn('grid gap-5', className)}
      noValidate
      {...props}
    >
      {error && (
        <div
          role='alert'
          className='rounded-xl border border-destructive/20 bg-destructive/8 px-4 py-3 text-sm text-destructive'
        >
          {error}
        </div>
      )}
      <Field
        id='konami-id'
        label='Konami ID'
        icon={IdCard}
        error={errors.konamiId?.message}
      >
        <input
          id='konami-id'
          placeholder='ABCD-1234-EFGH'
          autoComplete='username'
          autoCapitalize='characters'
          spellCheck={false}
          aria-invalid={!!errors.konamiId}
          className={inputClass}
          {...register('konamiId')}
        />
      </Field>
      <Field
        id='password'
        label='Password'
        icon={KeyRound}
        error={errors.password?.message}
      >
        <input
          id='password'
          type={showPassword ? 'text' : 'password'}
          placeholder='Your password'
          autoComplete='current-password'
          aria-invalid={!!errors.password}
          className={cn(inputClass, 'pe-12')}
          {...register('password')}
        />
        <button
          type='button'
          onClick={() => setShowPassword((shown) => !shown)}
          aria-label={showPassword ? 'Hide password' : 'Show password'}
          className='absolute end-2 top-1/2 grid size-8 -translate-y-1/2 place-items-center rounded-lg text-muted-foreground hover:bg-muted hover:text-foreground'
        >
          {showPassword ? (
            <EyeOff className='size-4' />
          ) : (
            <Eye className='size-4' />
          )}
        </button>
      </Field>
      <button
        type='submit'
        disabled={isLoading}
        className='group mt-1 flex h-12 items-center justify-center gap-2 rounded-xl bg-gradient-to-r from-[#5b5bd6] to-[#7272e6] text-sm font-semibold text-white shadow-[0_12px_28px_-10px_rgba(91,91,214,0.7)] transition hover:shadow-[0_16px_32px_-10px_rgba(91,91,214,0.8)] focus-visible:ring-4 focus-visible:ring-primary/25 focus-visible:outline-none disabled:opacity-70'
      >
        {isLoading ? (
          <Loader2 className='size-4 animate-spin' />
        ) : (
          <>
            Sign in
            <ArrowRight className='size-4 transition-transform group-hover:translate-x-0.5' />
          </>
        )}
      </button>
    </form>
  )
}
