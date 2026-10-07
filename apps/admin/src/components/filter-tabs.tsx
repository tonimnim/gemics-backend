import { humanize } from '@/lib/format'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'

/** Status tabs above a queue. */
export function FilterTabs({
  value,
  options,
  onChange,
}: {
  value: string
  options: readonly string[]
  onChange: (value: string) => void
}) {
  return (
    <Tabs value={value} onValueChange={onChange} className='mb-4'>
      <TabsList>
        {options.map((option) => (
          <TabsTrigger key={option} value={option}>
            {humanize(option)}
          </TabsTrigger>
        ))}
      </TabsList>
    </Tabs>
  )
}
