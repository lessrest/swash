(use-modules (ice-9 match))

(define task-tag (make-prompt-tag 'task))

(define (info . items)
  (abort-to-prompt task-tag 'display)
  (newline)
  (display items)
  (newline))

(define (nest id continuation)
  (call-with-prompt task-tag
	continuation
	(match-lambda*
	  ((k 'display)
	   (begin
		 (abort-to-prompt task-tag 'display)
		 (display id)
		 (display " ")
		 (nest id k))))))

(define (root continuation)
  (call-with-prompt task-tag 
	continuation
	(match-lambda*
	  ((k 'display)
	   (display "% ")
	   (root k)))))

(root 
	(lambda ()
	  (nest 'foo (lambda ()
				   (info "hello")
				   (nest 'bar (lambda ()
								(info "world")))))))
